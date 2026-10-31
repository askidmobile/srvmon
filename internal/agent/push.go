package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// Config of the agent loop.
type Config struct {
	URL        string        // panel base URL, e.g. https://mon.example.com
	Token      string        // agent token from `srvmon-server add-server`
	Interval   time.Duration // default 5s
	HostProc   string
	HostRoot   string
	DockerSock string
	Version    string
	MaxBuffer  int // reports kept while the panel is unreachable; default 720 (1h at 5s)
}

// Run collects and pushes until ctx is done.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	if cfg.URL == "" || cfg.Token == "" {
		return fmt.Errorf("SRVMON_URL and SRVMON_TOKEN are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.MaxBuffer <= 0 {
		cfg.MaxBuffer = 720
	}
	hostname := readHostname(cfg.HostRoot)
	col := &Collector{HostProc: cfg.HostProc, HostRoot: cfg.HostRoot}
	var dock *Docker
	if cfg.DockerSock != "" {
		if _, err := os.Stat(cfg.DockerSock); err == nil {
			dock = NewDocker(cfg.DockerSock)
		} else {
			log.Warn("docker socket not available, containers will not be reported", "sock", cfg.DockerSock)
		}
	}
	client := &http.Client{Timeout: 15 * time.Second}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/ingest"

	// Prime CPU counters so the first report has a real percentage.
	_, _ = col.Collect()
	var buf []protocol.Report
	t := time.NewTicker(cfg.Interval)
	defer t.Stop()
	log.Info("srvmon-agent started", "url", cfg.URL, "interval", cfg.Interval, "docker", dock != nil, "version", cfg.Version)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		rep := protocol.Report{Hostname: hostname, AgentVersion: cfg.Version, Timestamp: time.Now().UTC()}
		h, err := col.Collect()
		if err != nil {
			log.Warn("collect", "err", err)
		}
		rep.Host = h
		if dock != nil {
			cctx, cancel := context.WithTimeout(ctx, 4*time.Second)
			cs, err := dock.Containers(cctx)
			cancel()
			if err != nil {
				log.Warn("docker", "err", err)
			} else {
				rep.Containers, rep.DockerOK = cs, true
			}
		}
		buf = append(buf, rep)
		if len(buf) > cfg.MaxBuffer {
			buf = buf[len(buf)-cfg.MaxBuffer:]
		}
		// When catching up, send at most maxBatchReports reports and stay
		// well under the server's decompressed body limit.
		batch := nextBatch(buf, maxBatchReports, maxBatchBytes)
		if err := push(ctx, client, endpoint, cfg.Token, batch); err != nil {
			log.Warn("push failed, will retry", "err", err, "buffered", len(buf))
			continue
		}
		buf = buf[len(batch):]
	}
}

const (
	maxBatchReports = 100
	// The server accepts 4 MiB of decompressed JSON per request.
	maxBatchBytes = 2 << 20
)

// nextBatch returns the oldest reports that fit into maxBytes of JSON
// (always at least one, so a single large report still goes out).
func nextBatch(buf []protocol.Report, maxReports, maxBytes int) []protocol.Report {
	size := 2 // []
	for i, r := range buf {
		if i == maxReports {
			return buf[:i]
		}
		b, err := json.Marshal(r)
		if err != nil {
			return buf[:max(i, 1)]
		}
		size += len(b) + 1
		if size > maxBytes && i > 0 {
			return buf[:i]
		}
	}
	return buf
}

func push(ctx context.Context, c *http.Client, url, token string, reports []protocol.Report) error {
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if err := json.NewEncoder(zw).Encode(reports); err != nil {
		return err
	}
	if err := zw.Close(); err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	return nil
}

func readHostname(root string) string {
	if b, err := os.ReadFile(root + "/etc/hostname"); err == nil {
		if h := strings.TrimSpace(string(b)); h != "" {
			return h
		}
	}
	h, _ := os.Hostname()
	return h
}
