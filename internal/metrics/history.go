package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// HistoryPoints is how many buckets a history graph has (btop-like bars).
const HistoryPoints = 96

// Ranges are the graph windows the server screen offers.
var Ranges = map[string]time.Duration{
	"15m": 15 * time.Minute,
	"1h":  time.Hour,
	"6h":  6 * time.Hour,
	"24h": 24 * time.Hour,
	"7d":  7 * 24 * time.Hour,
}

// History is a set of graph series over one window, HistoryPoints buckets
// each; a nil value means no data in that bucket.
type History struct {
	Range   string     `json:"range"`
	Start   time.Time  `json:"start"`
	StepSec float64    `json:"step_sec"`
	CPU     []*float64 `json:"cpu"`
	Rx      []*float64 `json:"rx"`
	Tx      []*float64 `json:"tx"`
}

// sample is one value at a time.
type sample struct {
	t time.Time
	v float64
}

// buckets averages samples into n buckets of [start, start+n*step).
func buckets(start time.Time, step time.Duration, n int, ss []sample) []*float64 {
	sum := make([]float64, n)
	cnt := make([]int, n)
	for _, s := range ss {
		i := int(s.t.Sub(start) / step)
		if i < 0 || i >= n || math.IsNaN(s.v) || math.IsInf(s.v, 0) {
			continue
		}
		sum[i] += s.v
		cnt[i]++
	}
	out := make([]*float64, n)
	for i := range out {
		if cnt[i] > 0 {
			v := sum[i] / float64(cnt[i])
			out[i] = &v
		}
	}
	return out
}

// BuildHistory merges VictoriaMetrics series (may be nil) with in-memory
// points and buckets them. Memory points are used where VictoriaMetrics has
// nothing newer, so the live edge is never empty.
func BuildHistory(rangeName string, end time.Time, vm map[string][]sample, mem []Point) History {
	span := Ranges[rangeName]
	step := span / HistoryPoints
	start := end.Add(-span)
	merge := func(series string, pick func(Point) *float64) []sample {
		ss := vm[series]
		var last time.Time
		if n := len(ss); n > 0 {
			last = ss[n-1].t
		}
		for _, p := range mem {
			if !p.Time.After(last) {
				continue
			}
			if v := pick(p); v != nil {
				ss = append(ss, sample{p.Time, *v})
			}
		}
		return ss
	}
	cpu := func(p Point) *float64 { v := p.CPU; return &v }
	return History{
		Range:   rangeName,
		Start:   start,
		StepSec: step.Seconds(),
		CPU:     buckets(start, step, HistoryPoints, merge("cpu", cpu)),
		Rx:      buckets(start, step, HistoryPoints, merge("rx", func(p Point) *float64 { return p.Rx })),
		Tx:      buckets(start, step, HistoryPoints, merge("tx", func(p Point) *float64 { return p.Tx })),
	}
}

// Reader queries VictoriaMetrics.
type Reader struct {
	base   string
	client *http.Client
}

// NewReader returns a reader for the VictoriaMetrics base URL, or nil if url is empty.
func NewReader(u string) *Reader {
	if u == "" {
		return nil
	}
	w := NewWriter(u)
	return &Reader{base: w.base, client: &http.Client{Timeout: 10 * time.Second}}
}

// ServerSeries loads the graph series of one server for the window ending at end.
func (r *Reader) ServerSeries(ctx context.Context, server, rangeName string, end time.Time) (map[string][]sample, error) {
	if r == nil {
		return nil, nil
	}
	span := Ranges[rangeName]
	step := span / HistoryPoints
	// rate() needs at least two samples (5 s apart) inside its window.
	win := max(step, time.Minute)
	sel := fmt.Sprintf(`{server="%s"}`, escape(server))
	queries := map[string]string{
		"cpu": "srvmon_cpu_percent" + sel,
		"rx":  fmt.Sprintf("sum(rate(srvmon_net_rx_bytes_total%s[%ds]))", sel, int(win.Seconds())),
		"tx":  fmt.Sprintf("sum(rate(srvmon_net_tx_bytes_total%s[%ds]))", sel, int(win.Seconds())),
	}
	out := map[string][]sample{}
	for name, q := range queries {
		ss, err := r.queryRange(ctx, q, end.Add(-span), end, step)
		if err != nil {
			return nil, err
		}
		out[name] = ss
	}
	return out, nil
}

func (r *Reader) queryRange(ctx context.Context, q string, start, end time.Time, step time.Duration) ([]sample, error) {
	v := url.Values{}
	v.Set("query", q)
	v.Set("start", strconv.FormatInt(start.Unix(), 10))
	v.Set("end", strconv.FormatInt(end.Unix(), 10))
	v.Set("step", strconv.Itoa(max(1, int(step.Seconds())))+"s")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/api/v1/query_range?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("victoriametrics query: %s", resp.Status)
	}
	var body struct {
		Data struct {
			Result []struct {
				Values [][2]any `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	if len(body.Data.Result) == 0 {
		return nil, nil
	}
	var out []sample
	for _, pair := range body.Data.Result[0].Values {
		ts, ok1 := pair[0].(float64)
		vs, ok2 := pair[1].(string)
		if !ok1 || !ok2 {
			continue
		}
		val, err := strconv.ParseFloat(vs, 64)
		if err != nil {
			continue
		}
		out = append(out, sample{time.UnixMilli(int64(ts * 1000)), val})
	}
	return out, nil
}
