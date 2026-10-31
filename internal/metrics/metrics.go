// Package metrics converts agent reports into time series, writes them to
// VictoriaMetrics and keeps the latest report per server in memory for the
// dashboard.
package metrics

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// Writer sends Prometheus text exposition to VictoriaMetrics.
type Writer struct {
	base   string
	client *http.Client
}

// NewWriter returns a writer for the VictoriaMetrics base URL, or nil if url is empty.
func NewWriter(url string) *Writer {
	if url == "" {
		return nil
	}
	return &Writer{base: strings.TrimRight(url, "/"), client: &http.Client{Timeout: 10 * time.Second}}
}

// Write converts reports of one server and writes them in a single request,
// so a catch-up batch costs one round trip to VictoriaMetrics.
func (w *Writer) Write(ctx context.Context, server string, reports ...protocol.Report) error {
	if w == nil || len(reports) == 0 {
		return nil
	}
	var body []byte
	for _, r := range reports {
		body = append(body, Encode(server, r)...)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.base+"/api/v1/import/prometheus", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain")
	resp, err := w.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("victoriametrics: %s: %s", resp.Status, msg)
	}
	return nil
}

// Encode renders a report as Prometheus text lines with explicit timestamps.
func Encode(server string, r protocol.Report) []byte {
	ts := r.Timestamp.UnixMilli()
	if r.Timestamp.IsZero() {
		ts = time.Now().UnixMilli()
	}
	var b bytes.Buffer
	line := func(name string, labels [][2]string, v float64) {
		b.WriteString(name)
		b.WriteString(`{server="`)
		b.WriteString(escape(server))
		b.WriteByte('"')
		for _, l := range labels {
			fmt.Fprintf(&b, `,%s="%s"`, l[0], escape(l[1]))
		}
		fmt.Fprintf(&b, "} %g %d\n", v, ts)
	}
	h := r.Host
	line("srvmon_cpu_percent", nil, h.CPUPercent)
	line("srvmon_cpu_cores", nil, float64(h.CPUCores))
	line("srvmon_load1", nil, h.Load1)
	line("srvmon_load5", nil, h.Load5)
	line("srvmon_load15", nil, h.Load15)
	line("srvmon_mem_total_bytes", nil, float64(h.MemTotal))
	line("srvmon_mem_used_bytes", nil, float64(h.MemUsed()))
	line("srvmon_swap_total_bytes", nil, float64(h.SwapTotal))
	line("srvmon_swap_used_bytes", nil, float64(h.SwapTotal-min(h.SwapFree, h.SwapTotal)))
	line("srvmon_uptime_seconds", nil, h.UptimeSec)
	for _, d := range h.Disks {
		l := [][2]string{{"mount", d.Mount}, {"fs", d.FS}}
		line("srvmon_disk_total_bytes", l, float64(d.Total))
		line("srvmon_disk_used_bytes", l, float64(d.Used))
	}
	for _, n := range h.Net {
		l := [][2]string{{"iface", n.Name}}
		line("srvmon_net_rx_bytes_total", l, float64(n.RxBytes))
		line("srvmon_net_tx_bytes_total", l, float64(n.TxBytes))
	}
	if r.DockerOK {
		states := map[string]int{}
		for _, c := range r.Containers {
			states[c.State]++
			up := 0.0
			if c.State == "running" {
				up = 1
			}
			line("srvmon_container_up", [][2]string{{"container", c.Name}, {"image", c.Image}, {"health", c.Health}}, up)
			// Usage is known only while the server screen is open.
			if c.CPUPercent != nil {
				line("srvmon_container_cpu_percent", [][2]string{{"container", c.Name}}, *c.CPUPercent)
			}
			if c.MemBytes != nil {
				line("srvmon_container_mem_bytes", [][2]string{{"container", c.Name}}, float64(*c.MemBytes))
			}
		}
		keys := make([]string, 0, len(states))
		for k := range states {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			line("srvmon_containers", [][2]string{{"state", k}}, float64(states[k]))
		}
	}
	return b.Bytes()
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func escape(s string) string { return labelEscaper.Replace(s) }
