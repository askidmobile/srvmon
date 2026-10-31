package alerts

import (
	"bufio"
	"context"
	"io"
	"os"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/store"
)

// Fail2banEvent is one ban written by the srvmon-notify fail2ban action
// (deploy/fail2ban/action.d/srvmon-notify.conf):
//
//	2026-10-31T09:00:00Z ban jail=srvmon-auth ip=203.0.113.7 failures=5
type Fail2banEvent struct {
	Jail     string
	IP       string
	Failures string
}

// parseFail2ban parses one line; ok=false for anything but a ban.
func parseFail2ban(line string) (Fail2banEvent, bool) {
	f := strings.Fields(line)
	if len(f) < 3 || f[1] != "ban" {
		return Fail2banEvent{}, false
	}
	var ev Fail2banEvent
	for _, kv := range f[2:] {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "jail":
			ev.Jail = v
		case "ip":
			ev.IP = v
		case "failures":
			ev.Failures = v
		}
	}
	return ev, ev.IP != ""
}

// TailFail2ban follows the fail2ban events file and turns bans into
// fail2ban events. It starts at the end of the file (old bans are not
// replayed) and survives rotation and truncation. A missing file is waited
// for: install-host.sh creates it.
func (e *Engine) TailFail2ban(ctx context.Context, path, hostName string) {
	var off int64 = -1 // -1: skip to the end on first open
	var partial string
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	var prev os.FileInfo
	for {
		f, err := os.Open(path)
		if err == nil {
			st, err := f.Stat()
			if err != nil {
				f.Close()
				st = nil
			}
			switch {
			case st == nil:
			case off < 0:
				off = st.Size()
			case (prev != nil && !os.SameFile(prev, st)) || st.Size() < off:
				off, partial = 0, "" // rotated or truncated
			}
			if st != nil {
				prev = st
			}
			if st != nil && st.Size() > off {
				if _, err := f.Seek(off, io.SeekStart); err == nil {
					r := bufio.NewReader(io.LimitReader(f, st.Size()-off))
					for {
						line, err := r.ReadString('\n')
						off += int64(len(line))
						if err != nil {
							partial += line
							break
						}
						line, partial = partial+line, ""
						if ev, ok := parseFail2ban(strings.TrimSpace(line)); ok {
							msg := "fail2ban забанил " + ev.IP + " (jail " + ev.Jail
							if ev.Failures != "" {
								msg += ", попыток " + ev.Failures
							}
							msg += ")"
							e.Event(ctx, store.KindFail2ban, 0, hostName, ev.IP, msg)
						}
					}
				}
			}
			if st != nil {
				f.Close()
			}
		} else if off < 0 {
			off = 0 // file appears later: read it from the start
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
