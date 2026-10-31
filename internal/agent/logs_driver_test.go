package agent

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// A container whose logging driver cannot be read back is asked once, not
// on every pass (each refused request makes dockerd log an error).
func TestLogTailerSkipsUnreadableDriver(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Skip("unix sockets unavailable:", err)
	}
	var calls atomic.Int32
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotImplemented)
		_, _ = w.Write([]byte(`{"message":"configured logging driver does not support reading"}`))
	})}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })

	tl := &LogTailer{HostRoot: t.TempDir(), Docker: NewDocker(sock)}
	cs := []protocol.Container{{ID: "abc", Name: "web", State: "running"}}
	for range 3 {
		tl.Collect(context.Background(), cs)
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("logs requested %d times, want 1", n)
	}
	// Recreated container (new id): asked again; the old id is forgotten.
	tl.Collect(context.Background(), []protocol.Container{{ID: "def", Name: "web", State: "running"}})
	if n := calls.Load(); n != 2 || tl.noLogs["abc"] {
		t.Errorf("calls=%d noLogs=%v", n, tl.noLogs)
	}
}
