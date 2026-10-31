package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/askidmobile/srvmon/internal/protocol"
)

func TestParseStat(t *testing.T) {
	in := []byte("cpu  100 0 100 700 100 0 0 0 0 0\ncpu0 50 0 50 350 50 0 0 0 0 0\ncpu1 50 0 50 350 50 0 0 0 0 0\nintr 1\n")
	total, idle, cores, err := parseStat(in)
	if err != nil {
		t.Fatal(err)
	}
	if total != 1000 || idle != 800 || cores != 2 {
		t.Fatalf("got total=%d idle=%d cores=%d", total, idle, cores)
	}
}

func TestCollectorCPUPercent(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("meminfo", "MemTotal: 1000 kB\nMemAvailable: 250 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n")
	write("loadavg", "0.50 0.40 0.30 1/100 123\n")
	write("uptime", "3600.5 100.0\n")
	write("net/dev", "Inter-|\n face |\n  lo: 1 0 0 0 0 0 0 0 1 0 0 0 0 0 0 0\neth0: 2000 10 0 0 0 0 0 0 3000 10 0 0 0 0 0 0\n")
	write("stat", "cpu 100 0 0 900 0 0 0 0\ncpu0 100 0 0 900 0 0 0 0\n")
	c := &Collector{HostProc: dir, HostRoot: dir}
	if _, err := c.Collect(); err != nil {
		t.Fatal(err)
	}
	write("stat", "cpu 150 0 0 950 0 0 0 0\ncpu0 150 0 0 950 0 0 0 0\n")
	h, err := c.Collect()
	if err != nil {
		t.Fatal(err)
	}
	if h.CPUPercent != 50 {
		t.Errorf("cpu = %v, want 50", h.CPUPercent)
	}
	if h.MemTotal != 1000*1024 || h.MemUsed() != 750*1024 {
		t.Errorf("mem total=%d used=%d", h.MemTotal, h.MemUsed())
	}
	if h.Load1 != 0.5 || h.UptimeSec != 3600.5 {
		t.Errorf("load=%v uptime=%v", h.Load1, h.UptimeSec)
	}
	if len(h.Net) != 1 || h.Net[0].Name != "eth0" || h.Net[0].RxBytes != 2000 || h.Net[0].TxBytes != 3000 {
		t.Errorf("net = %+v", h.Net)
	}
}

func TestHealthFromStatus(t *testing.T) {
	for in, want := range map[string]string{
		"Up 3 hours (healthy)":            "healthy",
		"Up 1 minute (unhealthy)":         "unhealthy",
		"Up 2 seconds (health: starting)": "starting",
		"Exited (0) 2 days ago":           "",
	} {
		if got := healthFromStatus(in); got != want {
			t.Errorf("%q: got %q want %q", in, got, want)
		}
	}
}

func TestNextBatchRespectsSize(t *testing.T) {
	var buf []protocol.Report
	for i := 0; i < 50; i++ {
		r := protocol.Report{Hostname: "h"}
		for j := 0; j < 100; j++ {
			r.Containers = append(r.Containers, protocol.Container{Name: "container-with-a-long-name", Image: "registry.example.com/some/image:tag", Status: "Up 3 hours (healthy)"})
		}
		buf = append(buf, r)
	}
	one, _ := json.Marshal(buf[0])
	limit := len(one)*10 + 100
	b := nextBatch(buf, 100, limit)
	if len(b) == 0 || len(b) > 10 {
		t.Fatalf("batch of %d reports for limit of ~10", len(b))
	}
	if got := nextBatch(buf, 5, 1<<30); len(got) != 5 {
		t.Fatalf("count cap: %d", len(got))
	}
	if got := nextBatch(buf, 100, 1); len(got) != 1 {
		t.Fatalf("oversized single report must still be sent: %d", len(got))
	}
}

func TestReloadToken(t *testing.T) {
	f := filepath.Join(t.TempDir(), "tok")
	if tok, ch := reloadToken("", "a"); ch || tok != "a" {
		t.Fatal("no file must keep the token")
	}
	if tok, ch := reloadToken(f, "a"); ch || tok != "a" {
		t.Fatal("missing file must keep the token")
	}
	_ = os.WriteFile(f, []byte("a\n"), 0o600)
	if _, ch := reloadToken(f, "a"); ch {
		t.Fatal("same token reported as changed")
	}
	_ = os.WriteFile(f, []byte("b\n"), 0o600)
	if tok, ch := reloadToken(f, "a"); !ch || tok != "b" {
		t.Fatalf("got %q %v, want b true", tok, ch)
	}
}
