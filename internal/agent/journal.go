package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"strconv"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// journalLines caps one journalctl read.
const journalLines = 200

// journal returns warn-or-worse journald entries since the previous call;
// the first call looks back one hour. The agent runs journalctl itself (a
// fixed read-only command, never anything the panel sends).
func (t *LogTailer) journal(ctx context.Context) []protocol.LogLine {
	if t.noJournal {
		return nil
	}
	bin, err := exec.LookPath("journalctl")
	if err != nil {
		t.noJournal = true
		return nil
	}
	args := []string{"-q", "--no-pager", "-o", "json", "-p", "warning", "-n", strconv.Itoa(journalLines)}
	if t.journalCursor != "" {
		args = append(args, "--after-cursor="+t.journalCursor)
	} else {
		args = append(args, "--since=-1h")
	}
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if err != nil && len(out) == 0 {
		if ctx.Err() == nil {
			t.noJournal = true
		}
		return nil
	}
	lines, cursor := parseJournal(out)
	if cursor != "" {
		t.journalCursor = cursor
	}
	return lines
}

// parseJournal reads journalctl -o json output: one object per line.
// It returns the lines and the cursor of the last entry.
func parseJournal(out []byte) ([]protocol.LogLine, string) {
	var lines []protocol.LogLine
	cursor := ""
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		var e map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		if c := jstr(e["__CURSOR"]); c != "" {
			cursor = c
		}
		msg := jstr(e["MESSAGE"])
		if msg == "" {
			continue
		}
		var lvl string
		switch prio, _ := strconv.Atoi(jstr(e["PRIORITY"])); {
		case prio <= 3:
			lvl = "ERR"
		case prio == 4:
			lvl = "WARN"
		default:
			continue
		}
		us, _ := strconv.ParseInt(jstr(e["__REALTIME_TIMESTAMP"]), 10, 64)
		ts := time.UnixMicro(us).UTC()
		if us == 0 {
			ts = time.Now().UTC()
		}
		src := jstr(e["SYSLOG_IDENTIFIER"])
		if src == "" {
			src = jstr(e["_COMM"])
		}
		if src == "" {
			src = "journal"
		}
		lines = append(lines, protocol.LogLine{Time: ts, Level: lvl, Source: src, Msg: clip(msg, maxLogMsg)})
	}
	return lines, cursor
}

// jstr decodes a journald JSON field: a string, or an array of bytes for
// values that are not valid UTF-8.
func jstr(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var b []byte
	var ints []int
	if json.Unmarshal(raw, &ints) == nil {
		b = make([]byte, 0, len(ints))
		for _, v := range ints {
			b = append(b, byte(v))
		}
		return string(bytes.ToValidUTF8(b, []byte("?")))
	}
	return ""
}
