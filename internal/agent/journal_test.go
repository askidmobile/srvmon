package agent

import (
	"testing"
	"time"
)

func TestParseJournal(t *testing.T) {
	out := []byte(`{"__CURSOR":"s=1;i=1","__REALTIME_TIMESTAMP":"1761984000000000","PRIORITY":"3","SYSLOG_IDENTIFIER":"pvedaemon","MESSAGE":"authentication failure"}
{"__CURSOR":"s=1;i=2","__REALTIME_TIMESTAMP":"1761984001000000","PRIORITY":"4","_COMM":"zed","MESSAGE":[104,105,255]}
not json
{"__CURSOR":"s=1;i=3","PRIORITY":"6","MESSAGE":"info line"}
{"__CURSOR":"s=1;i=4","PRIORITY":"2","MESSAGE":""}
`)
	lines, cursor := parseJournal(out)
	if cursor != "s=1;i=4" {
		t.Errorf("cursor %q", cursor)
	}
	if len(lines) != 2 {
		t.Fatalf("lines %+v", lines)
	}
	if l := lines[0]; l.Level != "ERR" || l.Source != "pvedaemon" || l.Msg != "authentication failure" ||
		!l.Time.Equal(time.Unix(1761984000, 0)) {
		t.Errorf("first %+v", l)
	}
	if l := lines[1]; l.Level != "WARN" || l.Source != "zed" || l.Msg != "hi?" {
		t.Errorf("second %+v", l)
	}
}
