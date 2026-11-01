package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strconv"
	"time"

	"github.com/askidmobile/srvmon/internal/protocol"
)

// journalLines caps one journalctl read. Info entries are read too, since a
// service's stdout lands at PRIORITY=6 even when the line says [WARNING].
// The read goes forward from the cursor and stops after this many entries;
// the next pass continues where it stopped, so a burst of info lines delays
// a warning but cannot push it out of the window.
const journalLines = 5000

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
	// No -n: with it journalctl shows the newest entries of the range,
	// dropping the oldest. Without it the output runs forward from the
	// cursor and the read below stops early.
	args := []string{"-q", "--no-pager", "-o", "json", "-p", "info"}
	if t.journalCursor != "" {
		args = append(args, "--after-cursor="+t.journalCursor)
	} else {
		args = append(args, "--since=-1h")
	}
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil
	}
	if err := cmd.Start(); err != nil {
		t.noJournal = true
		return nil
	}
	lines, cursor, n := parseJournal(out, journalLines)
	cancel() // stops journalctl if the read ended early
	werr := cmd.Wait()
	if werr != nil && n == 0 && ctx.Err() == nil && cctx.Err() == nil {
		t.noJournal = true
	}
	if cursor != "" {
		t.journalCursor = cursor
	}
	return lines
}

// parseJournal reads journalctl -o json output, one object per line, up to
// max entries. It returns the warn-or-worse lines, the cursor of the last
// entry read and how many entries were read.
func parseJournal(r io.Reader, max int) ([]protocol.LogLine, string, int) {
	var lines []protocol.LogLine
	cursor := ""
	n := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for n < max && sc.Scan() {
		var e map[string]json.RawMessage
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		n++
		if c := jstr(e["__CURSOR"]); c != "" {
			cursor = c
		}
		msg := jstr(e["MESSAGE"])
		if msg == "" {
			continue
		}
		lvl := journalLevel(msg, jstr(e["PRIORITY"]))
		if lvl == "" {
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
	return lines, cursor, n
}

// journalLevel picks the level of a journald entry. PRIORITY is what the
// sender declared, but everything a service prints to stdout or stderr gets
// the unit's default (info) whatever it says, so a level the application
// wrote into the line counts as well, and the more severe of the two wins.
// Words in the message do not: "no 10002 errors" at PRIORITY=6 is info.
func journalLevel(msg, priority string) string {
	lvl := ""
	if p, err := strconv.Atoi(priority); err == nil {
		switch {
		case p <= 3:
			lvl = "ERR"
		case p == 4:
			lvl = "WARN"
		}
	}
	if app, ok := explicitLevel(msg); ok {
		if out, known := levelName(app); known && severity(out) > severity(lvl) {
			lvl = out
		}
	}
	return lvl
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
