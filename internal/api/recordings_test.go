package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/store"
)

func recEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.loginSSH(t)
	e.srv.cfg.DataDir = t.TempDir()
	return e
}

func TestRecordingLifecycle(t *testing.T) {
	e := recEnv(t)
	a := store.SSHAccess{ServerID: 7, Name: "web-1", User: "srvmon"}
	rec := e.srv.startRecording(a, "srvmon@web-1", "203.0.113.5", 80, 24)
	if rec == nil {
		t.Fatal("recording did not start")
	}
	rec.Output([]byte("$ cat .env\r\nTOKEN=xyz\r\n"))
	rec.Resize(120, 40)
	rec.finish()

	raw, _ := os.ReadFile(e.srv.recPath(rec.file))
	if strings.Contains(string(raw), "TOKEN") {
		t.Fatal("recording stored in plaintext")
	}

	var list []store.Recording
	resp := e.get(t, "/api/recordings?server=7")
	json.NewDecoder(resp.Body).Decode(&list)
	resp.Body.Close()
	if len(list) != 1 || list[0].Target != "srvmon@web-1" || list[0].EndedAt == nil || list[0].Bytes != 23 {
		t.Fatalf("list %+v", list)
	}

	code, body := e.raw(t, "GET", "/api/recordings/"+itoa(list[0].ID)+"/cast", nil)
	if code != http.StatusOK || !strings.HasPrefix(string(body), `{"version":2,"width":80,"height":24`) ||
		!strings.Contains(string(body), `TOKEN=xyz`) || !strings.Contains(string(body), `"r", "120x40"`) {
		t.Fatalf("cast %d %s", code, body)
	}
	req, _ := http.NewRequest("GET", e.ts.URL+"/api/recordings/"+itoa(list[0].ID)+"/cast?download=1", nil)
	dl, _ := e.client.Do(req)
	dl.Body.Close()
	if cd := dl.Header.Get("Content-Disposition"); !strings.Contains(cd, "srvmon-web-1-") || !strings.HasSuffix(cd, ".cast") {
		t.Fatalf("disposition %q", cd)
	}

	if r := e.do(t, "DELETE", "/api/recordings/"+itoa(list[0].ID), nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete %d", r.StatusCode)
	}
	if _, err := os.Stat(e.srv.recPath(rec.file)); !os.IsNotExist(err) {
		t.Fatal("file left after delete")
	}
}

func TestRecordingDisabled(t *testing.T) {
	e := recEnv(t)
	r := e.do(t, "PUT", "/api/recordings/settings", map[string]any{"enabled": false, "retention_days": 7, "max_mb": 10})
	if r.StatusCode != http.StatusOK {
		t.Fatalf("save %d", r.StatusCode)
	}
	if rec := e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "a"}, "a", "", 80, 24); rec != nil {
		t.Fatal("recorded while disabled")
	}
	var v recSettingsView
	resp := e.get(t, "/api/recordings/settings")
	json.NewDecoder(resp.Body).Decode(&v)
	resp.Body.Close()
	if v.Enabled || v.RetentionDays != 7 || v.MaxMB != 10 || !v.Available {
		t.Fatalf("settings %+v", v)
	}
	for _, bad := range []map[string]any{
		{"enabled": true, "retention_days": 0, "max_mb": 10},
		{"enabled": true, "retention_days": 7, "max_mb": 100000},
	} {
		if r := e.do(t, "PUT", "/api/recordings/settings", bad); r.StatusCode != http.StatusBadRequest {
			t.Errorf("%v: %d", bad, r.StatusCode)
		}
	}
}

func TestRecordingRetention(t *testing.T) {
	e := recEnv(t)
	now := time.Now()
	e.srv.now = func() time.Time { return now.AddDate(0, 0, -40) }
	old := e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "a"}, "a", "", 80, 24)
	old.finish()
	e.srv.now = func() time.Time { return now }
	fresh := e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "a"}, "a", "", 80, 24)
	fresh.finish()
	running := e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "a"}, "a", "", 80, 24)
	defer running.finish()
	// An orphan file from a crash.
	orphan := e.srv.recPath("deadbeef")
	os.WriteFile(orphan, []byte("x"), 0o600)
	os.Chtimes(orphan, now.Add(-2*time.Hour), now.Add(-2*time.Hour))

	e.srv.purgeRecordings(context.Background())
	list, _ := e.st.ListRecordings(context.Background(), 0, 0, 10)
	if len(list) != 2 {
		t.Fatalf("left %d recordings", len(list))
	}
	if _, err := os.Stat(e.srv.recPath(old.file)); !os.IsNotExist(err) {
		t.Error("expired file kept")
	}
	if _, err := os.Stat(orphan); !os.IsNotExist(err) {
		t.Error("orphan file kept")
	}
	if _, err := os.Stat(e.srv.recPath(running.file)); err != nil {
		t.Error("running recording removed")
	}
}

func TestStaleRecordingsClosedOnStart(t *testing.T) {
	e := recEnv(t)
	rec := e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "a"}, "a", "", 80, 24)
	rec.rec.Close() // the process died: no finish()
	if err := e.st.CloseStaleRecordings(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	r, _ := e.st.GetRecording(context.Background(), rec.id)
	if r.EndedAt == nil {
		t.Fatal("still running")
	}
}

func TestRecordingPagesAndServers(t *testing.T) {
	e := recEnv(t)
	for i := 0; i < recPageSize+5; i++ {
		e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "busy"}, "busy", "", 80, 24).finish()
	}
	e.srv.startRecording(store.SSHAccess{ServerID: 2, Name: "quiet"}, "quiet", "", 80, 24).finish()
	e.srv.startRecording(store.SSHAccess{ServerID: 1, Name: "busy"}, "busy", "", 80, 24).finish()

	var servers []store.RecordingServer
	resp := e.get(t, "/api/recordings/servers")
	json.NewDecoder(resp.Body).Decode(&servers)
	resp.Body.Close()
	if len(servers) != 2 || servers[0].Name != "busy" || servers[0].Count != recPageSize+6 || servers[1].Count != 1 {
		t.Fatalf("servers %+v", servers)
	}

	var page []store.Recording
	resp = e.get(t, "/api/recordings?server=2")
	json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if len(page) != 1 || page[0].ServerName != "quiet" {
		t.Fatalf("filtered %+v", page)
	}

	resp = e.get(t, "/api/recordings")
	json.NewDecoder(resp.Body).Decode(&page)
	resp.Body.Close()
	if len(page) != recPageSize {
		t.Fatalf("first page %d", len(page))
	}
	var rest []store.Recording
	resp = e.get(t, "/api/recordings?before="+itoa(page[len(page)-1].ID))
	json.NewDecoder(resp.Body).Decode(&rest)
	resp.Body.Close()
	if len(rest) != 7 || rest[0].ID >= page[len(page)-1].ID {
		t.Fatalf("second page %d", len(rest))
	}
}
