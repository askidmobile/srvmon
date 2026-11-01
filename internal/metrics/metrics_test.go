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

func TestEncodeSmartAndContainerUsage(t *testing.T) {
	ok := true
	hours, used := uint64(100), uint64(3)
	cpu, mem := 12.5, uint64(2048)
	r := protocol.Report{
		Timestamp:  time.UnixMilli(1700000000000),
		DockerOK:   true,
		Containers: []protocol.Container{{Name: "web", State: "running", CPUPercent: &cpu, MemBytes: &mem}},
		Smart: []protocol.DiskHealth{
			{Device: "nvme0n1", Model: "WD", Passed: &ok, PowerOnHours: &hours, PercentUsed: &used},
			{Device: "sda", Error: "standby"},
		},
	}
	out := string(Encode("s", r))
	for _, want := range []string{
		`srvmon_container_cpu_percent{server="s",container="web"} 12.5`,
		`srvmon_container_mem_bytes{server="s",container="web"} 2048`,
		`srvmon_smart_passed{server="s",device="nvme0n1",model="WD"} 1`,
		`srvmon_smart_power_on_hours{server="s",device="nvme0n1",model="WD"} 100`,
		`srvmon_smart_percent_used{server="s",device="nvme0n1",model="WD"} 3`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in\n%s", want, out)
		}
	}
	if strings.Contains(out, `device="sda"`) {
		t.Error("a disk that was not read is written")
	}
}

func TestDiskForecastAndContainerSeries(t *testing.T) {
	var queries []string
	vm := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		queries = append(queries, q)
		switch r.URL.Path {
		case "/api/v1/query":
			io.WriteString(w, `{"data":{"result":[
				{"metric":{"server":"a","mount":"/"},"value":[1,"36000"]},
				{"metric":{"server":"a","mount":"/slow"},"value":[1,"1e9"]},
				{"metric":{"server":"b","mount":"/data"},"value":[1,"NaN"]},
				{"metric":{"server":"c","mount":"/full"},"value":[1,"0"]},
				{"metric":{"server":"c","mount":"/neg"},"value":[1,"-5"]}]}}`)
		case "/api/v1/query_range":
			io.WriteString(w, `{"data":{"result":[{"values":[[1700000900,"5"]]}]}}`)
		}
	}))
	defer vm.Close()
	r := NewReader(vm.URL)
	got, err := r.DiskForecast(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["a"]["/"] != 10 || len(got["a"]) != 1 || len(got["c"]) != 1 {
		t.Fatalf("forecast = %v", got)
	}
	// A full disk keeps its zero-hour forecast.
	if h, ok := got["c"]["/full"]; !ok || h != 0 {
		t.Fatalf("full disk forecast = %v %v", h, ok)
	}
	end := time.Unix(1700000900, 0)
	h, err := r.ContainerSeries(context.Background(), "a", `we"b`, "15m", end)
	if err != nil {
		t.Fatal(err)
	}
	if len(h.CPU) != HistoryPoints || len(h.Mem) != HistoryPoints {
		t.Fatalf("buckets: %d %d", len(h.CPU), len(h.Mem))
	}
	if v := h.CPU[HistoryPoints-1]; v == nil || *v != 5 {
		t.Fatalf("last cpu bucket = %v", v)
	}
	if !strings.Contains(queries[len(queries)-1], `container="we\"b"`) {
		t.Fatalf("query = %s", queries[len(queries)-1])
	}
	// Without VictoriaMetrics the graphs are empty, not an error.
	var none *Reader
	if h, err := none.ContainerSeries(context.Background(), "a", "web", "1h", end); err != nil || len(h.CPU) != HistoryPoints {
		t.Fatalf("nil reader: %v %d", err, len(h.CPU))
	}
}
