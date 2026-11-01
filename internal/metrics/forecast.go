package metrics

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	// ForecastEvery is how often disk fill forecasts are recomputed.
	ForecastEvery = 5 * time.Minute
	// forecastWindow is the growth window: the slope is a linear regression
	// over it (the same one predict_linear uses).
	forecastWindow = "6h"
	// forecastMinCoverage: a filesystem needs this much history in the
	// window before it gets a forecast, whatever the agent's interval.
	forecastMinCoverage = time.Hour
	// ForecastHorizon: forecasts further out than this are dropped.
	ForecastHorizon = 30 * 24 * time.Hour
)

// diskForecastQuery returns, per filesystem that is growing, the seconds
// until it is full at its growth rate over forecastWindow.
var diskForecastQuery = fmt.Sprintf(
	`(srvmon_disk_total_bytes - srvmon_disk_used_bytes) / (deriv(srvmon_disk_used_bytes[%[1]s]) > 0)`+
		` and (time() - tfirst_over_time(srvmon_disk_used_bytes[%[1]s])) > %[2]d`,
	forecastWindow, int(forecastMinCoverage.Seconds()))

// Forecasts holds the latest disk fill forecasts: hours until full per
// server name and mount point. Filesystems that are not growing are absent.
type Forecasts struct {
	mu   sync.RWMutex
	data map[string]map[string]float64
	at   time.Time
}

// NewForecasts returns an empty set.
func NewForecasts() *Forecasts { return &Forecasts{} }

// Server returns the forecasts of one server (mount -> hours until full) and
// when they were computed; nil when there is no forecast.
func (f *Forecasts) Server(name string) (map[string]float64, time.Time) {
	if f == nil {
		return nil, time.Time{}
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.data[name], f.at
}

// Set replaces all forecasts.
func (f *Forecasts) Set(data map[string]map[string]float64, at time.Time) {
	f.mu.Lock()
	f.data, f.at = data, at
	f.mu.Unlock()
}

// RunForecasts recomputes forecasts every ForecastEvery until ctx is done.
func RunForecasts(ctx context.Context, r *Reader, f *Forecasts, log *slog.Logger) {
	if r == nil || f == nil {
		return
	}
	update := func() {
		qctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		data, err := r.DiskForecast(qctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("disk forecast", "err", err)
			}
			return
		}
		f.Set(data, time.Now())
	}
	// Let agents push first after a restart.
	select {
	case <-ctx.Done():
		return
	case <-time.After(time.Minute):
	}
	update()
	t := time.NewTicker(ForecastEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			update()
		}
	}
}

// DiskForecast queries hours until full for every growing filesystem.
func (r *Reader) DiskForecast(ctx context.Context) (map[string]map[string]float64, error) {
	res, err := r.query(ctx, diskForecastQuery)
	if err != nil {
		return nil, err
	}
	out := map[string]map[string]float64{}
	for _, s := range res {
		srv, mount := s.Metric["server"], s.Metric["mount"]
		if srv == "" || mount == "" || !(s.Value >= 0) || math.IsInf(s.Value, 0) {
			continue
		}
		h := s.Value / 3600
		if h > ForecastHorizon.Hours() {
			continue
		}
		if out[srv] == nil {
			out[srv] = map[string]float64{}
		}
		out[srv][mount] = h
	}
	return out, nil
}

type instantSample struct {
	Metric map[string]string
	Value  float64
}

// query runs an instant query.
func (r *Reader) query(ctx context.Context, q string) ([]instantSample, error) {
	v := url.Values{}
	v.Set("query", q)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.base+"/api/v1/query?"+v.Encode(), nil)
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
				Metric map[string]string `json:"metric"`
				Value  [2]any            `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []instantSample
	for _, res := range body.Data.Result {
		vs, ok := res.Value[1].(string)
		if !ok {
			continue
		}
		val, err := strconv.ParseFloat(vs, 64)
		if err != nil {
			continue
		}
		out = append(out, instantSample{Metric: res.Metric, Value: val})
	}
	return out, nil
}
