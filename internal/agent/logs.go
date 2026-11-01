package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

const (
	maxLogLines     = 60        // per report
	maxLogMsg       = 400       // characters per line
	syslogTailBytes = 256 << 10 // read at most this much of syslog per pass
	dockerLogBytes  = 256 << 10 // per container per pass
)

var (
	errRe = regexp.MustCompile(`(?i)\b(emerg|alert|panic|fatal|crit|critical|err|error|errors|exception|failed|failure|segfault|oom|out of memory|killed process)\b`)
	// excRe is an exception class or a Python traceback: ValueError: ...,
	// java.io.IOException, Traceback (most recent call last).
	excRe  = regexp.MustCompile(`\b[A-Z][A-Za-z0-9]*(?:Error|Exception)\b|^Traceback \(most recent call last\)`)
	warnRe = regexp.MustCompile(`(?i)\b(warn|warning|timeout|timed out|denied|refused|invalid user|retry|deprecated|degraded)\b`)

	// emptyErrRe is an error field with no error in it: JSON "error": null
	// or "errors": [], logfmt error=<nil> or errors=0. Unquoted "key: 0"
	// is left alone ("connect failed: 0 bytes sent" is an error).
	emptyErrRe = regexp.MustCompile(`(?i)(?:["'](?:err|error|errors|exception|exceptions|failure|failures|failed)["']\s*:\s*(?:null|none|""|''|\[\s*\]|\{\s*\}|false|0)|\b(?:err|error|errors|failures|failed)=(?:null|none|nil|<nil>|""|0))(?:[\s,;})\]]|$)`)
	// negatedErrRe is a count of zero: "no 10002 errors", "0 failures",
	// "without errors". At most two words may sit between.
	negatedErrRe = regexp.MustCompile(`(?i)\b(?:no|zero|0|without|never)\s+(?:[\w.,/-]+\s+){0,2}?(?:errors?|failures?|exceptions?|failed)\b`)
)

// logfmtRe walks a logfmt line: quoted strings are matched (and skipped) as
// whole tokens, so a level=... inside a quoted msg is not taken for the
// line's own level field.
var logfmtRe = regexp.MustCompile(`"(?:[^"\\]|\\.)*"|(?:^|\s)(?:level|lvl|severity)="?([A-Za-z]+)`)

var (
	jsonPrefixRe = regexp.MustCompile(`^\s*(?:[\w.\-/]+(?:\[\d+\])?:\s*)?$`)
	logfmtMsgRe  = regexp.MustCompile(`(?:^|\s)msg=`)
	// textLevelRe finds level words for textLevel to vet.
	textLevelRe = regexp.MustCompile(`(?i)\b(emerg|emergency|alert|crit|critical|fatal|panic|severe|error|err|warning|warn|notice|info|debug|trace)\b`)
	// stampRe is what may come before a bare level word: a timestamp,
	// a pid, spaces.
	stampRe = regexp.MustCompile(`^[\d\s\-:.,TZ+/]*$`)
)

// textLevelScan is how far into a free-text line a level marker is looked
// for; later words are message content.
const textLevelScan = 100

// explicitLevel returns the level the application wrote: a field of a
// structured line (JSON "level"/"lvl"/"severity", logfmt) or a level
// marker of a text log format. ok is false when there is none.
func explicitLevel(msg string) (string, bool) {
	// JSON counts only when the whole line is the object, optionally after a
	// syslog tag ("app[12]: {...}"); a JSON payload at the end of free text
	// is message content.
	if i := strings.IndexByte(msg, '{'); i >= 0 && jsonPrefixRe.MatchString(msg[:i]) && strings.HasSuffix(strings.TrimSpace(msg), "}") {
		var obj map[string]any
		if json.Unmarshal([]byte(msg[i:]), &obj) == nil {
			for _, k := range []string{"level", "lvl", "severity", "levelname"} {
				if v, ok := obj[k].(string); ok {
					return strings.ToLower(v), true
				}
			}
			return "", false
		}
	}
	// logfmt: only lines that carry a msg= field, so "ERROR x: level=info"
	// in free text is not taken for structured.
	if logfmtMsgRe.MatchString(msg) {
		for _, m := range logfmtRe.FindAllStringSubmatch(msg, -1) {
			if m[1] != "" {
				return strings.ToLower(m[1]), true
			}
		}
	}
	return textLevel(msg)
}

// textLevel finds the level marker of common text formats near the start of
// a line: "[WARNING] ...", "nginx: [error] ...", "WARNING:root:...",
// "... - name - ERROR - ...", "2026-11-01 10:00:00 WARN 12 --- ...",
// "ts\tWARN\tmsg", "WARN[0000] ...". A bare word counts only in capitals,
// a bracketed one in any case; the first marker wins.
func textLevel(msg string) (string, bool) {
	head := msg
	if len(head) > textLevelScan {
		head = head[:textLevelScan]
	}
	for _, m := range textLevelRe.FindAllStringIndex(head, -1) {
		w := head[m[0]:m[1]]
		var before, after byte
		if m[0] > 0 {
			before = head[m[0]-1]
		}
		if m[1] < len(head) {
			after = head[m[1]]
		}
		if before == '[' && after == ']' {
			return strings.ToLower(w), true
		}
		if w != strings.ToUpper(w) {
			continue
		}
		switch {
		case after == ':' || after == '[':
		case before == '\t' && (after == '\t' || after == 0):
		case strings.HasSuffix(head[:m[0]], " - ") && strings.HasPrefix(head[m[1]:], " - "),
			strings.HasSuffix(head[:m[0]], " | ") && strings.HasPrefix(head[m[1]:], " | "):
		case (after == ' ' || after == '\t' || after == 0) && stampRe.MatchString(head[:m[0]]):
		default:
			continue
		}
		return strings.ToLower(w), true
	}
	return "", false
}

// levelName maps a level word to ERR, WARN or "" (info and below); known is
// false for words that are no level.
func levelName(lvl string) (out string, known bool) {
	switch lvl {
	case "emerg", "emergency", "alert", "panic", "fatal", "crit", "critical", "severe", "err", "error":
		return "ERR", true
	case "warn", "warning":
		return "WARN", true
	case "info", "notice", "debug", "trace":
		return "", true
	}
	return "", false
}

// logLevel classifies a line from a source without a priority of its own
// (the syslog file, docker logs); "" means info and is dropped. A level
// the application wrote wins over words in the message, so dockerd's
// level=warning msg="... failed ..." stays a warning and "INFO ... error=x"
// stays info. Without one, keywords decide, after zero counts and empty
// error fields ("no 10002 errors", "error": null) are taken out.
func logLevel(msg string) string {
	if lvl, ok := explicitLevel(msg); ok {
		if out, known := levelName(lvl); known {
			return out
		}
	}
	text := emptyErrRe.ReplaceAllString(msg, " ")
	text = negatedErrRe.ReplaceAllString(text, " ")
	switch {
	case errRe.MatchString(text), excRe.MatchString(text):
		return "ERR"
	case warnRe.MatchString(text):
		return "WARN"
	}
	return ""
}

// severity orders ERR > WARN > "".
func severity(lvl string) int {
	switch lvl {
	case "ERR":
		return 2
	case "WARN":
		return 1
	}
	return 0
}

// LogTailer collects new warn-or-worse lines from the host syslog file and
// docker container logs.
type LogTailer struct {
	HostRoot string
	Docker   *Docker
	// Journal falls back to journald (journalctl) when the host has no
	// syslog file, as on Proxmox VE or Debian 12 without rsyslog.
	Journal bool

	syslogPath  string
	syslogIno   uint64
	syslogOff   int64
	dockerSince map[string]time.Time // container id -> last line time
	// noLogs holds containers whose logging driver (none, syslog, journald,
	// gelf, ...) cannot be read back: asking again every pass only makes
	// dockerd log an error each time.
	noLogs map[string]bool

	journalCursor string
	noJournal     bool // journalctl is missing or fails
}

// Collect returns lines that appeared since the previous call. The first
// call looks back at the last hour of container logs and the tail of syslog.
func (t *LogTailer) Collect(ctx context.Context, containers []protocol.Container) []protocol.LogLine {
	var out []protocol.LogLine
	out = append(out, t.syslog()...)
	if t.syslogPath == "" && t.Journal {
		out = append(out, t.journal(ctx)...)
	}
	if t.Docker != nil {
		out = append(out, t.docker(ctx, containers)...)
	}
	if len(out) > maxLogLines {
		// Keep the newest; the order within a source is chronological.
		sortLogs(out)
		out = out[len(out)-maxLogLines:]
	}
	return out
}

func sortLogs(l []protocol.LogLine) {
	for i := 1; i < len(l); i++ { // few lines, insertion sort keeps it stable
		for j := i; j > 0 && l[j].Time.Before(l[j-1].Time); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}

func (t *LogTailer) syslog() []protocol.LogLine {
	if t.syslogPath == "" {
		for _, p := range []string{"var/log/syslog", "var/log/messages"} {
			full := filepath.Join(t.HostRoot, p)
			if _, err := os.Stat(full); err == nil {
				t.syslogPath = full
				break
			}
		}
		if t.syslogPath == "" {
			return nil
		}
	}
	f, err := os.Open(t.syslogPath)
	if err != nil {
		return nil
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	ino := inode(st)
	size := st.Size()
	first := t.syslogIno == 0 && t.syslogOff == 0
	if ino != t.syslogIno || size < t.syslogOff { // rotated or truncated
		t.syslogOff = 0
		t.syslogIno = ino
		if first {
			t.syslogOff = max(0, size-64<<10)
		}
	}
	start := max(t.syslogOff, size-syslogTailBytes)
	if start >= size {
		return nil
	}
	buf := make([]byte, size-start)
	n, _ := f.ReadAt(buf, start)
	buf = buf[:n]
	// Only consume whole lines; a partial last line is read next time.
	last := bytes.LastIndexByte(buf, '\n')
	if last < 0 {
		return nil
	}
	buf = buf[:last+1]
	t.syslogOff = start + int64(len(buf))
	var out []protocol.LogLine
	sc := bufio.NewScanner(bytes.NewReader(buf))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		if l, ok := parseSyslogLine(sc.Text(), time.Now()); ok {
			out = append(out, l)
		}
	}
	return out
}

// parseSyslogLine handles rsyslog's RFC 3339 format
// ("2026-10-31T08:10:02.123456+00:00 host prog[123]: msg") and the classic
// one ("Oct 31 08:10:02 host prog[123]: msg").
func parseSyslogLine(line string, now time.Time) (protocol.LogLine, bool) {
	var ts time.Time
	var rest string
	if f := strings.SplitN(line, " ", 2); len(f) == 2 {
		if t, err := time.Parse(time.RFC3339Nano, f[0]); err == nil {
			ts, rest = t, f[1]
		}
	}
	if ts.IsZero() && len(line) > 16 {
		if t, err := time.ParseInLocation(time.Stamp, line[:15], time.Local); err == nil {
			ts = t.AddDate(now.Year(), 0, 0)
			if ts.After(now.Add(24 * time.Hour)) { // December lines read in January
				ts = ts.AddDate(-1, 0, 0)
			}
			rest = strings.TrimSpace(line[15:])
		}
	}
	if ts.IsZero() {
		return protocol.LogLine{}, false
	}
	// rest: "host prog[pid]: message"
	_, rest, _ = strings.Cut(rest, " ")
	src, msg, ok := strings.Cut(rest, ": ")
	if !ok {
		src, msg = "syslog", rest
	}
	if i := strings.IndexByte(src, '['); i > 0 {
		src = src[:i]
	}
	lvl := logLevel(msg)
	if lvl == "" {
		return protocol.LogLine{}, false
	}
	return protocol.LogLine{Time: ts.UTC(), Level: lvl, Source: src, Msg: clip(msg, maxLogMsg)}, true
}

func clip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func (t *LogTailer) docker(ctx context.Context, containers []protocol.Container) []protocol.LogLine {
	if t.dockerSince == nil {
		t.dockerSince = map[string]time.Time{}
	}
	var out []protocol.LogLine
	seen := map[string]bool{}
	for _, c := range containers {
		if c.State != "running" {
			continue
		}
		seen[c.ID] = true
		if t.noLogs[c.ID] {
			continue
		}
		since, ok := t.dockerSince[c.ID]
		if !ok {
			since = time.Now().Add(-time.Hour)
		}
		lines, last, err := t.Docker.Logs(ctx, c.ID, since)
		if errors.Is(err, errLogsUnsupported) {
			if t.noLogs == nil {
				t.noLogs = map[string]bool{}
			}
			t.noLogs[c.ID] = true
			continue
		}
		if err != nil {
			continue
		}
		if last.After(since) {
			t.dockerSince[c.ID] = last
		} else if !ok {
			t.dockerSince[c.ID] = time.Now()
		}
		for _, l := range lines {
			l.Source = c.Name
			out = append(out, l)
		}
	}
	for id := range t.dockerSince {
		if !seen[id] {
			delete(t.dockerSince, id)
		}
	}
	for id := range t.noLogs {
		if !seen[id] {
			delete(t.noLogs, id)
		}
	}
	return out
}

// errLogsUnsupported: the container's logging driver keeps no logs dockerd
// can read back.
var errLogsUnsupported = errors.New("logging driver does not support reading")

// Logs returns warn+ lines of a container written after since, and the
// timestamp of the newest line read.
func (d *Docker) Logs(ctx context.Context, id string, since time.Time) ([]protocol.LogLine, time.Time, error) {
	// since has second precision in the API; lines at exactly `since` are
	// filtered by timestamp below.
	u := fmt.Sprintf("http://docker/containers/%s/logs?stdout=1&stderr=1&timestamps=1&tail=500&since=%d",
		id, since.Unix())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, since, err
	}
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, since, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		if strings.Contains(string(b), "does not support reading") {
			return nil, since, errLogsUnsupported
		}
		return nil, since, fmt.Errorf("docker logs: %s", resp.Status)
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, dockerLogBytes))
	if err != nil {
		return nil, since, err
	}
	var out []protocol.LogLine
	last := since
	for _, line := range demuxDockerLogs(raw) {
		tsStr, msg, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, tsStr)
		if err != nil || !ts.After(since) {
			continue
		}
		if ts.After(last) {
			last = ts
		}
		if lvl := logLevel(msg); lvl != "" {
			out = append(out, protocol.LogLine{Time: ts.UTC(), Level: lvl, Msg: clip(msg, maxLogMsg)})
		}
	}
	return out, last, nil
}

// demuxDockerLogs splits the logs stream into lines. Containers without a TTY
// use the multiplexed format: 8-byte header [stream 0 0 0 size(be32)] + payload.
func demuxDockerLogs(b []byte) []string {
	var text bytes.Buffer
	if len(b) >= 8 && b[0] <= 2 && b[1] == 0 && b[2] == 0 && b[3] == 0 {
		for len(b) >= 8 {
			n := int(binary.BigEndian.Uint32(b[4:8]))
			b = b[8:]
			if n > len(b) {
				n = len(b)
			}
			text.Write(b[:n])
			b = b[n:]
		}
	} else {
		text.Write(b)
	}
	var out []string
	for _, l := range strings.Split(text.String(), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// RestartCount returns the restart count of a container from inspect.
func (d *Docker) RestartCount(ctx context.Context, id string) (int, error) {
	var v struct {
		RestartCount int `json:"RestartCount"`
	}
	if err := d.getJSON(ctx, "/containers/"+id+"/json", &v); err != nil {
		return 0, err
	}
	return v.RestartCount, nil
}

// Version returns the docker engine version.
func (d *Docker) Version(ctx context.Context) (string, error) {
	var v struct {
		Version string `json:"Version"`
	}
	if err := d.getJSON(ctx, "/version", &v); err != nil {
		return "", err
	}
	return v.Version, nil
}
