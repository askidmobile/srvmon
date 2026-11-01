package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pkg/sftp"

	"github.com/askidmobile/srvmon/internal/store"
)

// File manager over SFTP: the panel opens the sftp subsystem with the
// server's srvmon key, so the browser sees exactly what the srvmon user may
// read and write. Uploads arrive in chunks below the reverse proxy's body
// limit and are assembled in a hidden part file, renamed into place by the
// last chunk. Every change and download goes to the audit log.

const (
	filesIdle        = 90 * time.Second // an unused SFTP connection is closed
	filesOpTimeout   = 30 * time.Second
	filesMaxEntries  = 5000
	filesMaxChunk    = 4 << 20 // nginx in front allows 8 MiB bodies
	filesMaxPath     = 4096
	filesNewFileMode = 0o644          // mode of an uploaded file that replaces nothing
	filesPartMaxAge  = 24 * time.Hour // an abandoned upload's part file is removed after this
	filesChunkHeader = "X-Srvmon-Offset"
)

var (
	uploadIDRe = regexp.MustCompile(`^[0-9a-f]{16,32}$`)
	// partRe matches only part files srvmon names itself, so cleanup never
	// touches a user's file that merely looks similar.
	partRe = regexp.MustCompile(`^\.srvmon-upload-[0-9a-f]{16,32}\.part$`)
)

// partName is the hidden staging file of an upload, next to its target. It
// does not embed the target name, so any valid name still fits in 255 bytes.
func partName(target, uploadID string) string {
	return path.Join(path.Dir(target), ".srvmon-upload-"+uploadID+".part")
}

// sftpConn is one cached SFTP session to a server.
type sftpConn struct {
	key  string
	ssh  io.Closer // the SSH connection under the SFTP session
	c    *sftp.Client
	used time.Time
	refs int
	once sync.Once
}

// sftpPool keeps one SFTP connection per server while it is in use, so
// browsing a directory tree does not pay an SSH handshake per click.
type sftpPool struct {
	mu    sync.Mutex
	conns map[int64]*sftpConn
	dial  func(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error)
	now   func() time.Time
	stop  chan struct{}
	once  sync.Once
}

func newSFTPPool(dial func(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error)) *sftpPool {
	return &sftpPool{conns: map[int64]*sftpConn{}, dial: dial, now: time.Now, stop: make(chan struct{})}
}

// accessKey changes when the server's address, user or key changes, so a
// re-onboarded server never reuses the old connection.
func accessKey(a store.SSHAccess) string {
	return fmt.Sprintf("%s|%d|%s|%s|%x", a.Host, a.Port, a.User, a.HostKey, a.KeyEnc)
}

func (p *sftpPool) get(ctx context.Context, a store.SSHAccess) (*sftpConn, error) {
	p.once.Do(func() { go p.janitor() })
	key := accessKey(a)
	p.mu.Lock()
	if c := p.conns[a.ServerID]; c != nil {
		if c.key == key {
			c.refs++
			c.used = p.now()
			p.mu.Unlock()
			return c, nil
		}
		delete(p.conns, a.ServerID)
		if c.refs == 0 {
			go c.close()
		}
	}
	p.mu.Unlock()

	sc, fc, err := p.dial(ctx, a)
	if err != nil {
		return nil, err
	}
	c := &sftpConn{key: key, ssh: sc, c: fc, used: p.now(), refs: 1}
	p.mu.Lock()
	if old := p.conns[a.ServerID]; old != nil && old.refs == 0 {
		go old.close()
	}
	p.conns[a.ServerID] = c
	p.mu.Unlock()
	return c, nil
}

// put returns a connection; a connection-level error drops it.
func (p *sftpPool) put(serverID int64, c *sftpConn, err error) {
	broken := err != nil && (errors.Is(err, sftp.ErrSSHFxConnectionLost) || errors.Is(err, io.EOF) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled))
	p.mu.Lock()
	defer p.mu.Unlock()
	c.refs--
	c.used = p.now()
	cur := p.conns[serverID] == c
	if broken && cur {
		delete(p.conns, serverID)
		cur = false
	}
	if !cur && c.refs == 0 {
		go c.close()
	}
}

// kill closes a connection at once, even while in use: its pending calls
// fail and the pool dials anew for the next request.
func (p *sftpPool) kill(serverID int64, c *sftpConn) {
	p.mu.Lock()
	if p.conns[serverID] == c {
		delete(p.conns, serverID)
	}
	p.mu.Unlock()
	c.close()
}

// drop closes the connection of a deleted server.
func (p *sftpPool) drop(serverID int64) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.conns[serverID]; c != nil {
		delete(p.conns, serverID)
		if c.refs == 0 {
			go c.close()
		}
	}
}

func (p *sftpPool) janitor() {
	t := time.NewTicker(filesIdle / 3)
	defer t.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
		}
		p.mu.Lock()
		for id, c := range p.conns {
			if c.refs == 0 && p.now().Sub(c.used) > filesIdle {
				delete(p.conns, id)
				go c.close()
			}
		}
		p.mu.Unlock()
	}
}

func (c *sftpConn) close() {
	c.once.Do(c.closeNow)
}

// closeNow closes the SSH connection first: sftp.Client.Close waits for
// its reader, which only a closed transport unblocks on a stalled server.
func (c *sftpConn) closeNow() {
	if c.ssh != nil {
		c.ssh.Close()
	}
	c.c.Close()
}

func (s *Server) dialSFTP(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error) {
	sc, err := s.dialServer(ctx, a)
	if err != nil {
		return nil, nil, err
	}
	// NewClient waits for the server's version packet with no deadline.
	stop := context.AfterFunc(ctx, func() { sc.Close() })
	fc, err := sftp.NewClient(sc, sftp.UseConcurrentWrites(true))
	stop()
	if err != nil {
		sc.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, fmt.Errorf("sftp: %w", err)
	}
	return sc, fc, nil
}

func (s *Server) fileRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/servers/{id}/files", s.handleFilesList)
	h("GET /api/servers/{id}/files/download", s.handleFileDownload)
	h("POST /api/servers/{id}/files/upload", s.handleFileUpload)
	h("POST /api/servers/{id}/files/upload/abort", s.handleFileUploadAbort)
	h("POST /api/servers/{id}/files/mkdir", s.handleFileMkdir)
	h("POST /api/servers/{id}/files/rename", s.handleFileRename)
	h("POST /api/servers/{id}/files/delete", s.handleFileDelete)
}

// withSFTP resolves the server and runs fn with its SFTP client. fn returns
// the error that decides whether the connection is still good.
func (s *Server) withSFTP(w http.ResponseWriter, r *http.Request, timeout time.Duration,
	fn func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	a, err := s.store.ServerSSH(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_ssh_access")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ctx := r.Context()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	conn, err := s.files.get(ctx, a)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ssh", "message": err.Error()})
		return
	}
	// SFTP calls take no context: when the deadline passes or the client
	// goes away mid-call, close the connection, which fails every pending
	// call on it, and keep it out of the pool.
	stop := context.AfterFunc(ctx, func() { s.files.kill(id, conn) })
	err = fn(ctx, a, conn.c)
	stop()
	s.files.put(id, conn, err)
}

// cleanPath accepts absolute remote paths only.
func cleanPath(p string) (string, bool) {
	if p == "" || len(p) > filesMaxPath || p[0] != '/' || strings.ContainsRune(p, 0) {
		return "", false
	}
	return path.Clean(p), true
}

// validName is one path element for a new or renamed file.
func validName(n string) bool {
	return n != "" && n != "." && n != ".." && len(n) <= 255 && !strings.ContainsAny(n, "/\x00")
}

// sftpError maps an SFTP error to a response.
func sftpError(w http.ResponseWriter, err error) {
	var se *sftp.StatusError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, fs.ErrPermission):
		writeError(w, http.StatusForbidden, "permission")
	case errors.Is(err, fs.ErrExist):
		writeError(w, http.StatusConflict, "exists")
	case errors.As(err, &se):
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sftp", "message": se.Error()})
	default:
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "sftp", "message": err.Error()})
	}
}

type fileEntry struct {
	Name  string    `json:"name"`
	Size  int64     `json:"size"`
	Mode  string    `json:"mode"`
	MTime time.Time `json:"mtime"`
	Dir   bool      `json:"dir"`
	Link  bool      `json:"link,omitempty"`
}

type fileList struct {
	Path      string      `json:"path"`
	Entries   []fileEntry `json:"entries"`
	Truncated bool        `json:"truncated,omitempty"`
}

func (s *Server) handleFilesList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("path")
	s.withSFTP(w, r, filesOpTimeout, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		p := q
		if p == "" {
			home, err := c.RealPath(".")
			if err != nil {
				sftpError(w, err)
				return err
			}
			p = home
		}
		p, ok := cleanPath(p)
		if !ok {
			writeError(w, http.StatusBadRequest, "bad_path")
			return nil
		}
		infos, err := c.ReadDir(p)
		if err != nil {
			sftpError(w, err)
			return err
		}
		out := fileList{Path: p, Entries: make([]fileEntry, 0, len(infos))}
		for _, fi := range infos {
			if partRe.MatchString(fi.Name()) {
				sweepPart(c, p, fi) // upload in progress, or abandoned
				continue
			}
			e := fileEntry{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode().String(), MTime: fi.ModTime().UTC(), Dir: fi.IsDir()}
			if fi.Mode()&fs.ModeSymlink != 0 {
				e.Link = true
				// A link to a directory opens like one.
				if t, err := c.Stat(path.Join(p, fi.Name())); err == nil && t.IsDir() {
					e.Dir = true
				}
			}
			out.Entries = append(out.Entries, e)
		}
		sort.Slice(out.Entries, func(i, j int) bool {
			a, b := out.Entries[i], out.Entries[j]
			if a.Dir != b.Dir {
				return a.Dir
			}
			return strings.ToLower(a.Name) < strings.ToLower(b.Name)
		})
		if len(out.Entries) > filesMaxEntries {
			out.Entries, out.Truncated = out.Entries[:filesMaxEntries], true
		}
		writeJSON(w, http.StatusOK, out)
		return nil
	})
}

func (s *Server) handleFileDownload(w http.ResponseWriter, r *http.Request) {
	p, ok := cleanPath(r.URL.Query().Get("path"))
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	// No overall timeout: a large file may take long; the client going
	// away cancels the request context.
	s.withSFTP(w, r, 0, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		f, err := c.Open(p)
		if err != nil {
			sftpError(w, err)
			return err
		}
		defer f.Close()
		fi, err := f.Stat()
		if err != nil {
			sftpError(w, err)
			return err
		}
		if fi.IsDir() {
			writeError(w, http.StatusBadRequest, "is_dir")
			return nil
		}
		_ = s.store.Audit(context.Background(), s.clientIP(r), "file_download", a.Name+":"+p)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(p)))
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
		w.Header().Set("Cache-Control", "no-store")
		stop := context.AfterFunc(ctx, func() { f.Close() })
		defer stop()
		_, err = f.WriteTo(w)
		if err != nil && ctx.Err() != nil {
			err = ctx.Err()
		}
		return err
	})
}

// handleFileUpload takes one chunk: ?path=<file>&upload=<id>&offset=<n>
// &final=1, raw body. offset 0 starts the part file (and refuses an existing
// target unless &overwrite=1); a later chunk must continue where the part
// file ends. The client picks a random upload id per file, so two uploads
// to the same name never write into each other's part file.
func (s *Server) handleFileUpload(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := cleanPath(q.Get("path"))
	if !ok || !validName(path.Base(p)) {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	offset, err := strconv.ParseInt(q.Get("offset"), 10, 64)
	if err != nil || offset < 0 {
		writeError(w, http.StatusBadRequest, "bad_offset")
		return
	}
	uploadID := q.Get("upload")
	if !uploadIDRe.MatchString(uploadID) {
		writeError(w, http.StatusBadRequest, "bad_upload")
		return
	}
	final, overwrite := q.Get("final") == "1", q.Get("overwrite") == "1"
	body := http.MaxBytesReader(w, r.Body, filesMaxChunk)
	part := partName(p, uploadID)
	s.withSFTP(w, r, 2*time.Minute, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		if offset == 0 {
			if infos, err := c.ReadDir(path.Dir(p)); err == nil {
				for _, fi := range infos {
					if partRe.MatchString(fi.Name()) {
						sweepPart(c, path.Dir(p), fi)
					}
				}
			}
			if fi, err := c.Lstat(p); err == nil {
				if fi.IsDir() || !overwrite {
					writeError(w, http.StatusConflict, "exists")
					return nil
				}
			} else if !errors.Is(err, fs.ErrNotExist) {
				sftpError(w, err)
				return err
			}
		}
		flags := os.O_WRONLY | os.O_CREATE
		if offset == 0 {
			flags |= os.O_TRUNC
		}
		f, err := c.OpenFile(part, flags)
		if err != nil {
			sftpError(w, err)
			return err
		}
		defer f.Close()
		// The staging file is private until it is published: it may hold
		// a secret that replaces a 0600 file, in a directory others can read.
		if offset == 0 {
			if err := f.Chmod(0o600); err != nil {
				sftpError(w, err)
				return err
			}
		}
		if offset > 0 {
			fi, err := f.Stat()
			if err != nil {
				sftpError(w, err)
				return err
			}
			if fi.Size() != offset {
				writeJSON(w, http.StatusConflict, map[string]any{"error": "offset", "size": fi.Size()})
				return nil
			}
			if _, err := f.Seek(offset, io.SeekStart); err != nil {
				sftpError(w, err)
				return err
			}
		}
		n, err := f.ReadFrom(body)
		if err != nil {
			var mbe *http.MaxBytesError
			if errors.As(err, &mbe) {
				writeError(w, http.StatusRequestEntityTooLarge, "chunk_too_large")
				return nil
			}
			sftpError(w, err)
			return err
		}
		if err := f.Close(); err != nil {
			sftpError(w, err)
			return err
		}
		size := offset + n
		if !final {
			writeJSON(w, http.StatusOK, map[string]int64{"size": size})
			return nil
		}
		if err := finishUpload(c, part, p, overwrite); err != nil {
			// The target appeared while this upload ran (another upload
			// finished first): never replace it without overwrite=1.
			if _, serr := c.Lstat(p); serr == nil && !overwrite {
				_ = c.Remove(part)
				writeError(w, http.StatusConflict, "exists")
				return nil
			}
			sftpError(w, err)
			return err
		}
		_ = s.store.Audit(context.Background(), s.clientIP(r), "file_upload", fmt.Sprintf("%s:%s %d", a.Name, p, size))
		writeJSON(w, http.StatusOK, map[string]any{"size": size, "done": true})
		return nil
	})
}

// sweepPart removes the part file of an upload abandoned long ago (browser
// closed mid-upload): the listing hides part files, so nothing else would.
func sweepPart(c *sftp.Client, dir string, fi fs.FileInfo) {
	if time.Since(fi.ModTime()) > filesPartMaxAge {
		_ = c.Remove(path.Join(dir, fi.Name()))
	}
}

// finishUpload moves the part file into place. Without overwrite it uses
// the plain SFTP rename, which fails when the target exists; with it,
// posix-rename replaces atomically, or remove + rename on servers without
// that extension.
func finishUpload(c *sftp.Client, part, p string, overwrite bool) error {
	// The part file is 0600 while it fills. A replacement takes the mode of
	// the file it replaces (an executable stays executable, a secret stays
	// private); a new file gets the usual 0644.
	mode := os.FileMode(filesNewFileMode)
	if fi, err := c.Stat(p); err == nil && fi.Mode().IsRegular() {
		mode = fi.Mode().Perm()
	}
	if err := c.Chmod(part, mode); err != nil {
		return err
	}
	if !overwrite {
		return c.Rename(part, p)
	}
	if err := c.PosixRename(part, p); err == nil {
		return nil
	}
	_ = c.Remove(p)
	return c.Rename(part, p)
}

// handleFileUploadAbort removes the part file of a failed or cancelled
// upload: ?path=<file>&upload=<id>.
func (s *Server) handleFileUploadAbort(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p, ok := cleanPath(q.Get("path"))
	uploadID := q.Get("upload")
	if !ok || !validName(path.Base(p)) || !uploadIDRe.MatchString(uploadID) {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	part := partName(p, uploadID)
	s.withSFTP(w, r, filesOpTimeout, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		if err := c.Remove(part); err != nil && !errors.Is(err, fs.ErrNotExist) {
			sftpError(w, err)
			return err
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	})
}

type pathReq struct {
	Path      string `json:"path"`
	Recursive bool   `json:"recursive,omitempty"`
}

func (s *Server) handleFileMkdir(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json")
		return
	}
	p, ok := cleanPath(req.Path)
	if !ok || !validName(path.Base(p)) {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	s.withSFTP(w, r, filesOpTimeout, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		if _, err := c.Lstat(p); err == nil {
			writeError(w, http.StatusConflict, "exists")
			return nil
		}
		if err := c.Mkdir(p); err != nil {
			sftpError(w, err)
			return err
		}
		_ = s.store.Audit(context.Background(), s.clientIP(r), "file_mkdir", a.Name+":"+p)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	})
}

func (s *Server) handleFileRename(w http.ResponseWriter, r *http.Request) {
	var req struct {
		From string `json:"from"`
		To   string `json:"to"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json")
		return
	}
	from, ok1 := cleanPath(req.From)
	to, ok2 := cleanPath(req.To)
	if !ok1 || !ok2 || from == "/" || !validName(path.Base(to)) {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	s.withSFTP(w, r, filesOpTimeout, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		if _, err := c.Lstat(to); err == nil {
			writeError(w, http.StatusConflict, "exists")
			return nil
		}
		if err := c.Rename(from, to); err != nil {
			sftpError(w, err)
			return err
		}
		_ = s.store.Audit(context.Background(), s.clientIP(r), "file_rename", a.Name+":"+from+" -> "+to)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	})
}

func (s *Server) handleFileDelete(w http.ResponseWriter, r *http.Request) {
	var req pathReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json")
		return
	}
	p, ok := cleanPath(req.Path)
	if !ok || p == "/" {
		writeError(w, http.StatusBadRequest, "bad_path")
		return
	}
	s.withSFTP(w, r, 2*time.Minute, func(ctx context.Context, a store.SSHAccess, c *sftp.Client) error {
		fi, err := c.Lstat(p)
		if err != nil {
			sftpError(w, err)
			return err
		}
		switch {
		case !fi.IsDir():
			err = c.Remove(p)
		case req.Recursive:
			err = c.RemoveAll(p)
		default:
			err = c.RemoveDirectory(p)
		}
		if err != nil {
			sftpError(w, err)
			return err
		}
		_ = s.store.Audit(context.Background(), s.clientIP(r), "file_delete", a.Name+":"+p)
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return nil
	})
}
