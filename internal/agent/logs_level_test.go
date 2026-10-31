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
