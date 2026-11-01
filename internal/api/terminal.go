package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/crypto/ssh"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/sshgw"
	"github.com/askidmobile/srvmon/internal/store"
)

// Web terminal: xterm.js <-> WebSocket <-> SSH client here <-> sshd.
// Binary frames carry terminal bytes both ways; text frames from the browser
// are JSON control messages ({"type":"resize","cols":N,"rows":N}).

const (
	termReadLimit    = 64 << 10
	termWriteTimeout = 10 * time.Second
	termSessionCheck = time.Minute
	termSetupTimeout = 20 * time.Second
)

var containerRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type termControl struct {
	Type string `json:"type"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

func clampSize(cols, rows int) (int, int) {
	if cols < 10 || cols > 1000 {
		cols = 80
	}
	if rows < 2 || rows > 500 {
		rows = 24
	}
	return cols, rows
}

// wsWriter sends terminal output as binary frames and, when the session is
// recorded, to the recording.
type wsWriter struct {
	mu   sync.Mutex
	ctx  context.Context
	conn *websocket.Conn
	rec  *termRec // set before the shell starts
}

func (w *wsWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.rec.Output(p)
	ctx, cancel := context.WithTimeout(w.ctx, termWriteTimeout)
	defer cancel()
	if err := w.conn.Write(ctx, websocket.MessageBinary, p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// control sends a JSON control message to the browser as a text frame.
func (w *wsWriter) control(v any) {
	b, _ := json.Marshal(v)
	w.mu.Lock()
	defer w.mu.Unlock()
	ctx, cancel := context.WithTimeout(w.ctx, termWriteTimeout)
	defer cancel()
	_ = w.conn.Write(ctx, websocket.MessageText, b)
}

func (w *wsWriter) msg(text string) { _, _ = w.Write([]byte("\r\n\x1b[33m" + text + "\x1b[0m\r\n")) }

func (s *Server) handleTerminal(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	// WebSocket handshakes are GET requests, so checkOrigin does not cover
	// them: require a same-site Origin here (cross-site WebSocket hijacking).
	origin := r.Header.Get("Origin")
	if origin == "" || !s.originAllowed(origin, r.Host) {
		writeError(w, http.StatusForbidden, "bad_origin")
		return
	}
	container := r.URL.Query().Get("container")
	if container != "" && !containerRe.MatchString(container) {
		writeError(w, http.StatusBadRequest, "bad_container")
		return
	}
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	cols, rows = clampSize(cols, rows)

	access, err := s.store.ServerSSH(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_ssh_access")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	sessCookie, _ := r.Cookie(cookieSession)

	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true}) // origin checked above
	if err != nil {
		return
	}
	defer conn.CloseNow()
	conn.SetReadLimit(termReadLimit)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &wsWriter{ctx: ctx, conn: conn}
	ip := s.clientIP(r)

	target := access.Name
	if container != "" {
		target += ":" + container
	}
	out.msg(fmt.Sprintf("srvmon: ssh %s@%s:%d …", access.User, access.Host, access.Port))
	client, err := s.dialServer(ctx, access)
	if err != nil {
		out.msg(i18n.T("could not connect: %s", err.Error()))
		conn.Close(websocket.StatusNormalClosure, "dial failed")
		return
	}
	defer client.Close()
	// Session, PTY and shell setup has no deadline of its own: a host that
	// stops answering channel requests must not pin this handler. The timer
	// closes the connection, which unblocks every pending request.
	setup := time.AfterFunc(termSetupTimeout, func() { client.Close() })
	defer setup.Stop()
	stopOnCancel := context.AfterFunc(ctx, func() { client.Close() })
	defer stopOnCancel()

	sess, err := client.NewSession()
	if err != nil {
		out.msg("ssh session: " + err.Error())
		conn.Close(websocket.StatusNormalClosure, "session failed")
		return
	}
	defer sess.Close()
	modes := ssh.TerminalModes{ssh.ECHO: 1, ssh.TTY_OP_ISPEED: 14400, ssh.TTY_OP_OSPEED: 14400}
	if err := sess.RequestPty("xterm-256color", rows, cols, modes); err != nil {
		out.msg("pty: " + err.Error())
		conn.Close(websocket.StatusNormalClosure, "pty failed")
		return
	}
	stdin, err := sess.StdinPipe()
	if err != nil {
		return
	}
	recTarget := access.User + "@" + access.Name
	if container != "" {
		recTarget = target
	}
	rec := s.startRecording(access, recTarget, ip, cols, rows)
	started := false
	defer func() {
		if started {
			rec.finish()
		} else {
			rec.discard()
		}
	}()
	out.rec = rec
	sess.Stdout = out
	sess.Stderr = out
	if container != "" {
		err = sess.Start("docker exec -it " + container + " sh -c 'command -v bash >/dev/null 2>&1 && exec bash || exec sh'")
	} else {
		err = sess.Shell()
	}
	if err != nil {
		out.msg("shell: " + err.Error())
		conn.Close(websocket.StatusNormalClosure, "shell failed")
		return
	}
	if !setup.Stop() {
		out.msg(i18n.T("the server did not answer in time"))
		conn.Close(websocket.StatusNormalClosure, "setup timeout")
		return
	}
	started = true
	if rec != nil {
		out.control(map[string]any{"type": "rec", "on": true, "id": rec.id})
	}
	_ = s.store.Audit(context.Background(), ip, "terminal_open", target)
	s.log.Info("terminal open", "server", target, "ip", ip)
	opened := time.Now()

	// Browser -> SSH.
	go func() {
		defer cancel()
		for {
			typ, data, err := conn.Read(ctx)
			if err != nil {
				return
			}
			if typ == websocket.MessageBinary {
				if _, err := stdin.Write(data); err != nil {
					return
				}
				continue
			}
			var c termControl
			if json.Unmarshal(data, &c) == nil && c.Type == "resize" {
				cc, rr := clampSize(c.Cols, c.Rows)
				_ = sess.WindowChange(rr, cc)
				rec.Resize(cc, rr)
			}
		}
	}()
	// The panel session may expire or be logged out while the terminal is open.
	go func() {
		t := time.NewTicker(termSessionCheck)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if sessCookie == nil {
					cancel()
					return
				}
				kind, err := s.store.SessionKind(ctx, auth.HashToken(sessCookie.Value))
				if err != nil || kind != store.SessionFull {
					out.msg(i18n.T("the panel session expired"))
					cancel()
					return
				}
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- sess.Wait() }()
	reason := "exit"
	select {
	case <-done:
		out.msg(i18n.T("session ended"))
	case <-ctx.Done():
		reason = "closed"
	}
	_ = s.store.Audit(context.Background(), ip, "terminal_close",
		fmt.Sprintf("%s %s", target, time.Since(opened).Truncate(time.Second)))
	conn.Close(websocket.StatusNormalClosure, reason)
}

// dialServer opens an SSH connection with the server's stored key.
func (s *Server) dialServer(ctx context.Context, a store.SSHAccess) (*ssh.Client, error) {
	if s.secrets == nil {
		return nil, errors.New(i18n.T("the master key is not loaded"))
	}
	hostKey, err := sshgw.ParseHostKey(a.HostKey)
	if err != nil {
		return nil, fmt.Errorf("host key: %w", err)
	}
	priv, err := s.secrets.Open(a.KeyEnc, keyAAD(a.ServerID))
	if err != nil {
		return nil, err
	}
	defer clear(priv)
	return sshgw.DialKey(ctx, sshgw.Addr(a.Host, a.Port), a.User, hostKey, priv)
}

// handleContainerAction restarts (or stops/starts) a container over SSH.
func (s *Server) handleContainerAction(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	name, action := r.PathValue("name"), r.PathValue("action")
	if !containerRe.MatchString(name) {
		writeError(w, http.StatusBadRequest, "bad_container")
		return
	}
	switch action {
	case "restart", "stop", "start":
	default:
		writeError(w, http.StatusBadRequest, "bad_action")
		return
	}
	access, err := s.store.ServerSSH(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "no_ssh_access")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	client, err := s.dialServer(ctx, access)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "ssh", "detail": err.Error()})
		return
	}
	defer client.Close()
	_, errOut, err := sshgw.Run(ctx, client, "docker "+action+" "+name, nil)
	_ = s.store.Audit(context.Background(), s.clientIP(r), "container_"+action, access.Name+":"+name)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "docker", "detail": strings.TrimSpace(errOut)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
