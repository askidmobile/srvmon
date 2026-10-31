package metrics

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

func TestEncode(t *testing.T) {
	r := protocol.Report{
		Timestamp: time.UnixMilli(1700000000000),
		Host:      protocol.HostMetrics{CPUPercent: 12.5, MemTotal: 100, MemAvailable: 40},
		DockerOK:  true,
		Containers: []protocol.Container{
			{Name: "web", Image: "nginx", State: "running"},
			{Name: "job", Image: `we"ird`, State: "exited"},
		},
	}
	out := string(Encode(`shop"db`, r))
	for _, want := range []string{
		`srvmon_cpu_percent{server="shop\"db"} 12.5 1700000000000`,
		`srvmon_mem_used_bytes{server="shop\"db"} 60 1700000000000`,
		`srvmon_container_up{server="shop\"db",container="job",image="we\"ird",health=""} 0`,
		`srvmon_containers{server="shop\"db",state="running"} 1`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestWriteBatchIsOneRequest(t *testing.T) {
	var calls int
	var body string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer ts.Close()
	reports := make([]protocol.Report, 50)
	for i := range reports {
		reports[i].Timestamp = time.UnixMilli(int64(1700000000000 + i))
	}
	if err := NewWriter(ts.URL).Write(context.Background(), "s", reports...); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || strings.Count(body, "srvmon_cpu_percent") != 50 {
		t.Fatalf("calls=%d, cpu lines=%d", calls, strings.Count(body, "srvmon_cpu_percent"))
	}
}
