package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/pkg/sftp"

	"github.com/askidmobile/srvmon/internal/store"
)

// sftpEnv logs in, adds a server with SSH access and points the SFTP pool
// at an in-memory SFTP server.
func sftpEnv(t *testing.T) (*env, int64) {
	t.Helper()
	e := newEnv(t)
	e.login(t)
	ctx := context.Background()
	id, err := e.st.CreateServer(ctx, store.Server{Name: "web-1", Host: "192.0.2.10"})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetServerSSH(ctx, id, "192.0.2.10", 22, "srvmon", "ssh-ed25519 AAAA", []byte("k")); err != nil {
		t.Fatal(err)
	}
	h := sftp.InMemHandler()
	dials := 0
	e.srv.files = newSFTPPool(func(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error) {
		dials++
		cr, sw := io.Pipe()
		sr, cw := io.Pipe()
		srv := sftp.NewRequestServer(struct {
			io.Reader
			io.WriteCloser
		}{sr, sw}, h)
		go srv.Serve()
		c, err := sftp.NewClientPipe(cr, cw)
		return nil, c, err
	})
	t.Cleanup(func() {
		if dials > 1 {
			t.Errorf("dialed %d times, want one cached connection", dials)
		}
	})
	return e, id
}

func (e *env) raw(t *testing.T, method, path string, body io.Reader) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, e.ts.URL+path, body)
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func TestFilesRequireSession(t *testing.T) {
	e := newEnv(t)
	for _, c := range []struct{ method, path string }{
		{"GET", "/api/servers/1/files?path=/"},
		{"GET", "/api/servers/1/files/download?path=/etc/passwd"},
		{"POST", "/api/servers/1/files/upload?path=/x&offset=0"},
		{"POST", "/api/servers/1/files/delete"},
		{"GET", "/api/recordings"},
		{"GET", "/api/recordings/1/cast"},
		{"PUT", "/api/recordings/settings"},
	} {
		if code, _ := e.raw(t, c.method, c.path, strings.NewReader("{}")); code != http.StatusUnauthorized {
			t.Errorf("%s %s: %d", c.method, c.path, code)
		}
	}
}

func TestFilesFlow(t *testing.T) {
	e, id := sftpEnv(t)
	base := "/api/servers/" + itoa(id) + "/files"

	if code := e.postJSON(t, base+"/mkdir", map[string]string{"path": "/data"}, nil); code != http.StatusOK {
		t.Fatalf("mkdir: %d", code)
	}
	if code := e.postJSON(t, base+"/mkdir", map[string]string{"path": "/data"}, nil); code != http.StatusConflict {
		t.Fatalf("mkdir again: %d", code)
	}

	// Upload in two chunks.
	up := func(q string, body string) (int, []byte) {
		return e.raw(t, "POST", base+"/upload?"+q, strings.NewReader(body))
	}
	p := url.QueryEscape("/data/hello.txt")
	uid, uid2 := "0123456789abcdef", "fedcba9876543210"
	if code, _ := up("path="+p+"&offset=0", "x"); code != http.StatusBadRequest {
		t.Fatalf("no upload id: %d", code)
	}
	if code, b := up("path="+p+"&upload="+uid+"&offset=0", "hello, "); code != http.StatusOK {
		t.Fatalf("chunk 1: %d %s", code, b)
	}
	if code, _ := up("path="+p+"&upload="+uid+"&offset=3", "x"); code != http.StatusConflict {
		t.Fatalf("wrong offset: %d", code)
	}
	if code, b := up("path="+p+"&upload="+uid+"&offset=7&final=1", "world"); code != http.StatusOK {
		t.Fatalf("chunk 2: %d %s", code, b)
	}
	if code, _ := up("path="+p+"&upload="+uid2+"&offset=0&final=1", "again"); code != http.StatusConflict {
		t.Fatalf("existing file without overwrite: %d", code)
	}
	if code, _ := up("path="+url.QueryEscape("/data/..")+"&upload="+uid+"&offset=0", "x"); code != http.StatusBadRequest {
		t.Fatalf("bad name: %d", code)
	}

	var list fileList
	resp := e.get(t, base+"?path=/data")
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if list.Path != "/data" || len(list.Entries) != 1 || list.Entries[0].Name != "hello.txt" || list.Entries[0].Size != 12 {
		t.Fatalf("list %+v", list)
	}

	code, body := e.raw(t, "GET", base+"/download?path="+p, nil)
	if code != http.StatusOK || string(body) != "hello, world" {
		t.Fatalf("download: %d %q", code, body)
	}

	if code := e.postJSON(t, base+"/rename", map[string]string{"from": "/data/hello.txt", "to": "/data/hi.txt"}, nil); code != http.StatusOK {
		t.Fatalf("rename: %d", code)
	}
	if code, _ := e.raw(t, "GET", base+"/download?path="+p, nil); code != http.StatusNotFound {
		t.Fatalf("old name: %d", code)
	}
	if code := e.postJSON(t, base+"/delete", map[string]string{"path": "/data"}, nil); code == http.StatusOK {
		t.Fatal("non-empty dir deleted without recursive")
	}
	if code := e.postJSON(t, base+"/delete", map[string]any{"path": "/data", "recursive": true}, nil); code != http.StatusOK {
		t.Fatalf("delete: %d", code)
	}
	if code := e.postJSON(t, base+"/delete", map[string]string{"path": "/"}, nil); code != http.StatusBadRequest {
		t.Fatalf("delete /: %d", code)
	}
	if code, _ := e.raw(t, "GET", base+"?path=relative", nil); code != http.StatusBadRequest {
		t.Fatalf("relative path: %d", code)
	}
}

func TestUploadChunkLimit(t *testing.T) {
	e, id := sftpEnv(t)
	big := bytes.Repeat([]byte("a"), filesMaxChunk+1)
	code, _ := e.raw(t, "POST", "/api/servers/"+itoa(id)+"/files/upload?path=/big&upload=0123456789abcdef&offset=0", bytes.NewReader(big))
	if code != http.StatusRequestEntityTooLarge {
		t.Fatalf("code %d", code)
	}
}

func TestConcurrentUploadsDoNotMix(t *testing.T) {
	e, id := sftpEnv(t)
	base := "/api/servers/" + itoa(id) + "/files/upload?path=%2Ff&"
	a, b := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	e.raw(t, "POST", base+"upload="+a+"&offset=0", strings.NewReader("AAAA"))
	e.raw(t, "POST", base+"upload="+b+"&offset=0", strings.NewReader("BB"))
	if code, body := e.raw(t, "POST", base+"upload="+a+"&offset=4&final=1", strings.NewReader("aa")); code != http.StatusOK {
		t.Fatalf("A final: %d %s", code, body)
	}
	_, got := e.raw(t, "GET", "/api/servers/"+itoa(id)+"/files/download?path=%2Ff", nil)
	if string(got) != "AAAAaa" {
		t.Fatalf("got %q", got)
	}
}

func TestStalledSFTPCallIsCut(t *testing.T) {
	e, id := sftpEnv(t)
	e.srv.files = newSFTPPool(func(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error) {
		// A server that answers the handshake and then never again.
		cr, sw := io.Pipe()
		sr, cw := io.Pipe()
		go func() {
			buf := make([]byte, 64)
			sr.Read(buf)                                // client init
			sw.Write([]byte{0, 0, 0, 5, 2, 0, 0, 0, 3}) // SSH_FXP_VERSION 3
			io.Copy(io.Discard, sr)
		}()
		c, err := sftp.NewClientPipe(cr, cw)
		return sw, c, err // closing it ends the client's reader, like a dropped SSH link
	})
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/servers/"+itoa(id)+"/files?path=/", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	_, err := e.client.Do(req.WithContext(ctx))
	if err == nil {
		t.Fatal("expected the client to give up")
	}
	// The stalled connection is gone: the next request dials anew.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		e.srv.files.mu.Lock()
		n := len(e.srv.files.conns)
		e.srv.files.mu.Unlock()
		if n == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stalled connection kept in the pool")
}

func TestUploadFinishNeverReplacesSilently(t *testing.T) {
	e, id := sftpEnv(t)
	base := "/api/servers/" + itoa(id) + "/files/upload?path=%2Fg&"
	a, b := "aaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbb"
	// Both start while /g does not exist yet.
	e.raw(t, "POST", base+"upload="+a+"&offset=0", strings.NewReader("AAAA"))
	e.raw(t, "POST", base+"upload="+b+"&offset=0", strings.NewReader("BB"))
	if code, _ := e.raw(t, "POST", base+"upload="+a+"&offset=4&final=1", strings.NewReader("")); code != http.StatusOK {
		t.Fatalf("A final: %d", code)
	}
	if code, _ := e.raw(t, "POST", base+"upload="+b+"&offset=2&final=1", strings.NewReader("")); code != http.StatusConflict {
		t.Fatalf("B final without overwrite: %d", code)
	}
	_, got := e.raw(t, "GET", "/api/servers/"+itoa(id)+"/files/download?path=%2Fg", nil)
	if string(got) != "AAAA" {
		t.Fatalf("got %q", got)
	}
	// With overwrite the replacement goes through.
	e.raw(t, "POST", base+"upload="+b+"&offset=0&overwrite=1", strings.NewReader("BB"))
	if code, _ := e.raw(t, "POST", base+"upload="+b+"&offset=2&final=1&overwrite=1", strings.NewReader("")); code != http.StatusOK {
		t.Fatalf("B overwrite: %d", code)
	}
	_, got = e.raw(t, "GET", "/api/servers/"+itoa(id)+"/files/download?path=%2Fg", nil)
	if string(got) != "BB" {
		t.Fatalf("after overwrite %q", got)
	}
}

func TestUploadAbortRemovesPart(t *testing.T) {
	e, id := sftpEnv(t)
	base := "/api/servers/" + itoa(id) + "/files/upload"
	q := "?path=%2Fh&upload=cccccccccccccccc"
	e.raw(t, "POST", base+q+"&offset=0", strings.NewReader("half"))
	if code, _ := e.raw(t, "POST", base+"/abort"+q, nil); code != http.StatusOK {
		t.Fatalf("abort: %d", code)
	}
	// The part file is gone: continuing the upload finds nothing to append to.
	if code, _ := e.raw(t, "POST", base+q+"&offset=4&final=1", strings.NewReader("x")); code != http.StatusConflict {
		t.Fatalf("continue after abort: %d", code)
	}
}

// realFS points the pool at an SFTP server on the local file system and
// returns a scratch directory for the test's paths.
func realFS(t *testing.T, e *env) string {
	t.Helper()
	e.srv.files = newSFTPPool(func(ctx context.Context, a store.SSHAccess) (io.Closer, *sftp.Client, error) {
		cr, sw := io.Pipe()
		sr, cw := io.Pipe()
		srv, err := sftp.NewServer(struct {
			io.Reader
			io.WriteCloser
		}{sr, sw})
		if err != nil {
			return nil, nil, err
		}
		go srv.Serve()
		c, err := sftp.NewClientPipe(cr, cw)
		return sw, c, err
	})
	return t.TempDir()
}

func TestAbandonedPartsAreSwept(t *testing.T) {
	e, id := sftpEnv(t)
	dir := realFS(t, e)
	up := func(name, uid string) {
		q := url.Values{"path": {dir + "/" + name}, "upload": {uid}, "offset": {"0"}}
		e.raw(t, "POST", "/api/servers/"+itoa(id)+"/files/upload?"+q.Encode(), strings.NewReader("x"))
	}
	up("old", "dddddddddddddddd")
	up("new", "eeeeeeeeeeeeeeee")
	oldPart := partName(dir+"/old", "dddddddddddddddd")
	newPart := partName(dir+"/new", "eeeeeeeeeeeeeeee")
	// A user's own file that only looks like a part file.
	lookalike := dir + "/backup.srvmon-part"
	os.WriteFile(lookalike, []byte("keep"), 0o644)
	past := time.Now().Add(-2 * filesPartMaxAge)
	for _, f := range []string{oldPart, lookalike} {
		if err := os.Chtimes(f, past, past); err != nil {
			t.Fatal(err)
		}
	}
	if fi, err := os.Stat(newPart); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("staging file must be private: %v %v", fi, err)
	}
	e.raw(t, "GET", "/api/servers/"+itoa(id)+"/files?path="+url.QueryEscape(dir), nil)
	if _, err := os.Stat(oldPart); err == nil {
		t.Error("abandoned part file kept")
	}
	if _, err := os.Stat(newPart); err != nil {
		t.Error("running upload's part file removed")
	}
	if _, err := os.Stat(lookalike); err != nil {
		t.Error("user's file removed")
	}
}

func TestLongNameUploads(t *testing.T) {
	e, id := sftpEnv(t)
	dir := realFS(t, e)
	name := strings.Repeat("n", 250)
	q := url.Values{"path": {dir + "/" + name}, "upload": {strings.Repeat("f", 32)}, "offset": {"0"}, "final": {"1"}}
	if code, b := e.raw(t, "POST", "/api/servers/"+itoa(id)+"/files/upload?"+q.Encode(), strings.NewReader("x")); code != http.StatusOK {
		t.Fatalf("upload: %d %s", code, b)
	}
	if fi, err := os.Stat(dir + "/" + name); err != nil || fi.Mode().Perm() != filesNewFileMode {
		t.Fatalf("published file: %v %v", fi, err)
	}
}

func TestReplaceKeepsMode(t *testing.T) {
	e, id := sftpEnv(t)
	dir := realFS(t, e)
	target := dir + "/secret"
	if err := os.WriteFile(target, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	q := url.Values{"path": {target}, "upload": {"2222222222222222"}, "offset": {"0"}, "final": {"1"}, "overwrite": {"1"}}
	if code, b := e.raw(t, "POST", "/api/servers/"+itoa(id)+"/files/upload?"+q.Encode(), strings.NewReader("new")); code != http.StatusOK {
		t.Fatalf("replace: %d %s", code, b)
	}
	fi, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "new" || fi.Mode().Perm() != 0o600 {
		t.Fatalf("content %q mode %v", b, fi.Mode().Perm())
	}
}
