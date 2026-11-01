package aiagent

import (
	"strings"
	"testing"
)

func TestRedact(t *testing.T) {
	cases := []struct{ in, want string }{
		{"POSTGRES_PASSWORD=hunter2", "POSTGRES_PASSWORD=***"},
		{`"Env": ["POSTGRES_PASSWORD=hunter2", "TZ=UTC"]`, `"Env": ["POSTGRES_PASSWORD=***", "TZ=UTC"]`},
		{`"password": "hunter2",`, `"password": "***",`},
		{"  JWT_SECRET: 'abc def'", "  JWT_SECRET: '***'"},
		{"Environment=API_KEY=abc123 TZ=UTC", "Environment=API_KEY=*** TZ=UTC"},
		{"app --db-password=hunter2 --port=80", "app --db-password=*** --port=80"},
		{"DATABASE_URL=postgres://app:hunter2@db:5432/app", "DATABASE_URL=postgres://app:***@db:5432/app"},
		{"Authorization: Bearer abcdefghijklmnop", "Authorization: Bearer ***"},
		{"x ghp_abcdefghijklmnopqrstuvwxyz0123456789 y", "x *** y"},
		{"key sk-ant-api03-abcdefghijklmnopqrstuvwxyz", "key ***"},
		{"bot 123456789:AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsawq", "bot ***"},
		{`"Env": ["POSTGRES_PASSWORD=abc secret-rest", "TZ=UTC"]`, `"Env": ["POSTGRES_PASSWORD=***", "TZ=UTC"]`},
		{`"password": "ab\"c d"`, `"password": "***"`},
		{`"Env": ["DB_PASSWORD=a\"b c"]`, `"Env": ["DB_PASSWORD=***"]`},
		{"PASSWORD=$uperSecret123", "PASSWORD=***"},
		{"password: $2b$12$abcdefghijklmnopqrstuv", "password: ***"},
		{"PASSWORD: ${DB_PASSWORD:-changeme}", "PASSWORD: ${DB_PASSWORD:-changeme}"},
		{"PASSWORD=$DB_PASSWORD", "PASSWORD=$DB_PASSWORD"},
		{"DB_PASSWORD: abc,secret-rest", "DB_PASSWORD: ***"},
		{"PASSWORD=ab;cdef", "PASSWORD=***"},
		{"{password: abc, user: app}", "{password: ***, user: app}"},
		{"password=abc;", "password=***;"},
		// Configuration, not secrets: kept.
		{"PasswordAuthentication no", "PasswordAuthentication no"},
		{"auth_required: true", "auth_required: true"},
		{"PASSWORD: ${DB_PASSWORD}", "PASSWORD: ${DB_PASSWORD}"},
		{"TZ=UTC", "TZ=UTC"},
		{"Active: active (running) since Mon", "Active: active (running) since Mon"},
	}
	for _, c := range cases {
		if got := Redact(c.in); got != c.want {
			t.Errorf("Redact(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	key := "-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQ\nAAAAAAAAAAEAAAAzAAAAC3NzaC1lZDI1NTE5AAAAIF\n-----END OPENSSH PRIVATE KEY-----\nafter"
	if got := Redact(key); got != "[redacted private key]\nafter" {
		t.Errorf("private key: %q", got)
	}
	// Head cut off the BEGIN line: the tail still has the body and END.
	tail := "[… 100 bytes skipped …]\nAAAAAAAAAAEAAAAzAAAAC3NzaC1lZDI1NTE5AAAAIF\n-----END RSA PRIVATE KEY-----\n"
	if got := Redact(tail); strings.Contains(got, "AAAAzAAAA") {
		t.Errorf("key tail: %q", got)
	}
	// Tail cut off the END line.
	if got := Redact("-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASC"); strings.Contains(got, "MIIE") {
		t.Errorf("key head: %q", got)
	}
}
