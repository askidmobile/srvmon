package agent

import "testing"

func TestLogLevelHonorsExplicitLevel(t *testing.T) {
	for in, want := range map[string]string{
		`time="2026-10-31T23:59:14" level=warning msg="ShouldRestart failed, container will not be restarted"`: "WARN",
		`time="2026-11-01T02:13:28" level=error msg="failed to validate image signature"`:                      "ERR",
		`time="2026-11-01T02:13:28" level=info msg="restart failed attempts reset"`:                            "",
		`{"msg":"request failed with level=info","level":"error"}`:                                             "ERR",
		`dockerd: time="t" msg="retry with level=info" level=error`:                                            "ERR",
		`app: level="warning" msg="disk almost full"`:                                                          "WARN",
		`ERROR failed to process request body: {"level":"info"}`:                                               "ERR",
		`ERROR worker crashed: level=info`:                                                                     "ERR",
		`app[12]: {"level":"warn","msg":"slow"}`:                                                               "WARN",
		`{"level":"warn","msg":"slow query"}`:                                                                  "WARN",
		`kernel: Out of memory: Killed process 123`:                                                            "ERR",
		`sshd: Invalid user admin from 1.2.3.4`:                                                                "WARN",
		`systemd: Started session 5`:                                                                           "",
	} {
		if got := logLevel(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

// Lines from trading-agency (2026-11-01) that came out as ERR only because
// they contain the word "error", next to real errors that must stay.
func TestLogLevelNoFalseErrors(t *testing.T) {
	for in, want := range map[string]string{
		// zero counts and empty error fields
		`no 10002 errors in last 5m, nothing to do`:          "",
		`  "error": null,`:                                   "",
		`{"cycle": "ok", "error": null, "trades": 3}`:        "",
		`result: {"error": null, "errors": [], "failed": 0}`: "",
		`sync done: 0 errors, 0 failures`:                    "",
		`backup finished without errors`:                     "",
		`job=sync errors=0 err=<nil>`:                        "",
		`No errors found`:                                    "",
		// real errors stay errors
		`  "error": "connection reset by peer",`:          "ERR",
		`{"cycle": "degraded", "error": "redis timeout"}`: "ERR",
		`3 errors in last 5m: 10002 resync failed`:        "ERR",
		`no 10002 errors in last 5m, but snapshot failed`: "ERR",
		`Traceback: ValueError: bad price`:                "ERR",
		`connect to exchange failed: EOF`:                 "ERR",
		// a degraded cycle stays visible
		`cycle=degraded reason=stale_quotes`: "WARN",
		// levels written by the application
		`[WARNING] relay: queue backlog 1200`:                     "WARN",
		`2026-11-01 19:40:00,123 [WARNING] relay: queue backlog`:  "WARN",
		`2026-11-01 19:40:00,123 - relay - WARNING - backlog`:     "WARN",
		`WARNING:root:backlog`:                                    "WARN",
		`ERROR:root:order rejected`:                               "ERR",
		`[INFO] retry budget: 0 errors`:                           "",
		`INFO cycle done, error=none`:                             "",
		`2026-11-01T19:40:00.000Z	INFO	no errors, retry later`:    "",
		`2026-11-01T19:40:00.000Z	ERROR	order rejected`:           "ERR",
		`2026-11-01 19:40:00.123  WARN 1234 --- [main] slow`:      "WARN",
		`WARN[0000] deprecated flag`:                              "WARN",
		`nginx: 2026/11/01 19:40:00 [error] 12#12: upstream gone`: "ERR",
		`nginx: 2026/11/01 19:40:00 [notice] 12#12: reopening`:    "",
		`{"levelname": "WARNING", "message": "backlog"}`:          "WARN",
		// a capitalised word mid-sentence is no level marker
		`Reading the INFO file failed`: "ERR",
	} {
		if got := logLevel(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}

func TestJournalLevel(t *testing.T) {
	for _, c := range []struct{ msg, prio, want string }{
		// PRIORITY=6 without a level in the text: info, whatever the words.
		{`no 10002 errors in last 5m, nothing to do`, "6", ""},
		{`  "error": null,`, "6", ""},
		{`connect failed, will retry`, "6", ""},
		// Python's [WARNING] through stdout keeps PRIORITY=6.
		{`[WARNING] relay: queue backlog 1200`, "6", "WARN"},
		{`2026-11-01 19:40:00,123 - relay - ERROR - send failed`, "6", "ERR"},
		// The sender's priority counts; the more severe level wins.
		{`authentication failure`, "3", "ERR"},
		{`disk almost full`, "4", "WARN"},
		{`[INFO] shutting down after fatal signal`, "2", "ERR"},
		{`[ERROR] boom`, "4", "ERR"},
		{`plain line`, "", ""},
	} {
		if got := journalLevel(c.msg, c.prio); got != c.want {
			t.Errorf("%q prio=%s: %q, want %q", c.msg, c.prio, got, c.want)
		}
	}
}
