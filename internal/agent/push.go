package agent

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
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
	URL       string        // panel base URL, e.g. https://mon.example.com
	Token     string        // agent token from `srvmon-server add-server`
	TokenFile string        // if set, re-read after a failed push (token rotated by the server)
	Interval  time.Duration // default 5s
	// DetailInterval is the push interval while the server screen is open.
	DetailInterval time.Duration // default 2s
	HostProc       string
	HostRoot       string
	DockerSock     string
	Version        string
	MaxBuffer      int // reports kept while the panel is unreachable; default 720 (1h at 5s)
	// Smart enables S.M.A.R.T. reads of the host's disks. The container
	// then needs CAP_SYS_RAWIO, CAP_SYS_ADMIN and read access to the block
	// devices (see deploy/agent/docker-compose.yml).
	Smart bool
	// Mode is reported to the panel as Report.AgentMode.
	Mode string
	// WatchFiles makes Run return ErrRestart once one of these files is
	// replaced or changed (systemd mode: the panel swaps the binary or the
	// environment file and the service manager starts the new one).
	WatchFiles []string
	// Journal reads warn-or-worse lines from journald (journalctl) when the
	// host has no syslog file. Only for an agent running on the host itself.
	Journal bool
}

// ErrRestart: a watched file changed, the agent should exit and be started
// again by its service manager.
var ErrRestart = errors.New("agent files changed, restart")

// Run collects and pushes until ctx is done.
func Run(ctx context.Context, cfg Config, log *slog.Logger) error {
	if cfg.URL == "" || cfg.Token == "" {
		return fmt.Errorf("SRVMON_URL and SRVMON_TOKEN are required")
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 5 * time.Second
	}
	if cfg.DetailInterval <= 0 {
		cfg.DetailInterval = 2 * time.Second
	}
	if cfg.MaxBuffer <= 0 {
		cfg.MaxBuffer = 720
	}
	hostname := readHostname(cfg.HostRoot)
	col := &Collector{HostProc: cfg.HostProc, HostRoot: cfg.HostRoot}
	var dock *Docker
	// The socket may appear after the start (srvmon-dockerproxy starting
	// next to the agent): it is looked up again before every report.
	findDocker := func() {
		if dock != nil || cfg.DockerSock == "" {
			return
		}
		if _, err := os.Stat(cfg.DockerSock); err == nil {
			dock = NewDocker(cfg.DockerSock)
		}
	}
	findDocker()
	if dock == nil && cfg.DockerSock != "" {
		log.Warn("docker socket not available yet, containers are not reported until it appears", "sock", cfg.DockerSock)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	endpoint := strings.TrimRight(cfg.URL, "/") + "/api/ingest"

	det := &DetailCollector{HostProc: cfg.HostProc, HostRoot: cfg.HostRoot}
	logs := &LogTailer{HostRoot: cfg.HostRoot, Docker: dock, Journal: cfg.Journal}
	watch := newFileWatch(cfg.WatchFiles)
	cg := &CgroupUsage{HostRoot: cfg.HostRoot}
	smart := newSmartLoop(cfg.Smart, cfg.HostRoot, log)
	restarts := map[string]int{}
	var restartsAt, logsAt, invAt, tokenAt time.Time
	var detailUntil time.Time

	// Prime CPU counters so the first report has a real percentage.
	_, _ = col.Collect()
	var buf []protocol.Report
	timer := time.NewTimer(cfg.Interval)
	defer timer.Stop()
	log.Info("srvmon-agent started", "url", cfg.URL, "interval", cfg.Interval, "docker", dock != nil, "version", cfg.Version, "mode", cfg.Mode)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
		if f := watch.changed(); f != "" {
			log.Info("file changed, restarting", "file", f)
			return ErrRestart
		}
		now := time.Now()
		// A token the panel rewrites in the file (reissue) is picked up
		// within a minute even while the old one still works.
		if now.Sub(tokenAt) >= time.Minute {
			if tok, changed := reloadToken(cfg.TokenFile, cfg.Token); changed {
				cfg.Token = tok
				log.Info("agent token reloaded", "file", cfg.TokenFile)
			}
			tokenAt = now
		}
		detail := now.Before(detailUntil)
		if detail {
			timer.Reset(cfg.DetailInterval)
		} else {
			timer.Reset(cfg.Interval)
		}

		if dock == nil && cfg.DockerSock != "" {
			if findDocker(); dock != nil {
				logs.Docker = dock
				log.Info("docker socket available", "sock", cfg.DockerSock)
			}
		}
		rep := protocol.Report{Hostname: hostname, AgentVersion: cfg.Version, AgentMode: cfg.Mode, Timestamp: now.UTC()}
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
		// Container CPU and memory from cgroups on every report, so the
		// history has them even when nobody watches the server screen.
		var running []string
		for _, c := range rep.Containers {
			if c.State == "running" {
				running = append(running, c.ID)
			}
		}
		cgUsage := cg.Usage(running, now)
		for i := range rep.Containers {
			c := &rep.Containers[i]
			if u, ok := cgUsage[c.ID]; ok {
				m := u.MemBytes
				c.MemBytes = &m
				if !u.noCPU {
					v := u.CPUPercent
					c.CPUPercent = &v
				}
			}
		}
		rep.Smart = smart.collect(ctx, now)
		if now.Sub(invAt) >= time.Minute {
			ictx, cancel := context.WithTimeout(ctx, 3*time.Second)
			rep.Inventory = Inventory(ictx, cfg.HostProc, cfg.HostRoot, dock)
			rep.Inventory.Smart = cfg.Smart
			cancel()
			invAt = now
		}
		if detail {
			ids := make(map[string]string, len(rep.Containers))
			for _, c := range rep.Containers {
				ids[c.ID] = c.Name
			}
			d, usage := det.Collect(ids)
			if dock != nil && now.Sub(restartsAt) >= 30*time.Second {
				rctx, cancel := context.WithTimeout(ctx, 4*time.Second)
				for _, c := range rep.Containers {
					if n, err := dock.RestartCount(rctx, c.ID); err == nil {
						restarts[c.ID] = n
					}
				}
				cancel()
				restartsAt = now
			}
			for i := range rep.Containers {
				c := &rep.Containers[i]
				if n, ok := restarts[c.ID]; ok {
					c.Restarts = &n
				}
				if _, ok := cgUsage[c.ID]; !ok && c.State == "running" {
					// No cgroup found (unusual driver): sum the processes.
					u := usage[c.Name]
					c.CPUPercent, c.MemBytes = &u.CPUPercent, &u.MemBytes
				}
			}
			if now.Sub(logsAt) >= logsEvery {
				lctx, cancel := context.WithTimeout(ctx, 4*time.Second)
				d.Logs = logs.Collect(lctx, rep.Containers)
				cancel()
				logsAt = now
			}
			rep.Detail = d
		}
		buf = append(buf, rep)
		if len(buf) > cfg.MaxBuffer {
			buf = buf[len(buf)-cfg.MaxBuffer:]
		}
		// When catching up, send at most maxBatchReports reports and stay
		// well under the server's decompressed body limit.
		batch := nextBatch(buf, maxBatchReports, maxBatchBytes)
		resp, err := push(ctx, client, endpoint, cfg.Token, batch)
		if err != nil {
			log.Warn("push failed, will retry", "err", err, "buffered", len(buf))
			if tok, changed := reloadToken(cfg.TokenFile, cfg.Token); changed {
				cfg.Token = tok
				log.Info("agent token reloaded", "file", cfg.TokenFile)
			}
			continue
		}
		buf = buf[len(batch):]
		if resp.Smart {
			smart.request(time.Now())
		}
		if resp.Detail {
			if !detail {
				// Switch to the fast cadence right away and refresh the
				// passport (the server may have restarted and lost it).
				timer.Reset(cfg.DetailInterval)
				invAt = time.Time{}
			}
			detailUntil = time.Now().Add(detailHold)
		}
	}
}

const (
	// detailHold keeps detail mode on this long after the last server
	// request for it, so a short network hiccup does not flap it.
	detailHold = 15 * time.Second
	logsEvery  = 6 * time.Second
)

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

func push(ctx context.Context, c *http.Client, url, token string, reports []protocol.Report) (protocol.IngestResponse, error) {
	var out protocol.IngestResponse
	var body bytes.Buffer
	zw := gzip.NewWriter(&body)
	if err := json.NewEncoder(zw).Encode(reports); err != nil {
		return out, err
	}
	if err := zw.Close(); err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return out, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Content-Encoding", "gzip")
	resp, err := c.Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return out, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(msg)))
	}
	// An older server may answer without a body; that just means no detail.
	_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&out)
	return out, nil
}

// reloadToken returns the token from file when it differs from cur.
func reloadToken(file, cur string) (string, bool) {
	if file == "" {
		return cur, false
	}
	b, err := os.ReadFile(file)
	if err != nil {
		return cur, false
	}
	tok := strings.TrimSpace(string(b))
	if tok == "" || tok == cur {
		return cur, false
	}
	return tok, true
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
