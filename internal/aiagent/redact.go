package aiagent

import (
	"regexp"
	"strings"
)

// Redact masks secrets in command output before it reaches the model.
// "Read-only" only means a command changes nothing: `docker inspect`,
// `systemctl show`, `ps eww` or `grep -r` over a config directory still
// print passwords, and whatever the agent sees goes to the model provider
// (and, through a prompt injection, could go further). This is a best-effort
// second layer next to the secret-path check in Classify, not a guarantee:
// it catches KEY=value style secrets, credentials in URLs, bearer tokens,
// well-known token formats and private key blocks.
func Redact(s string) string {
	if s == "" {
		return s
	}
	s = privateKeyBlock.ReplaceAllString(s, "[redacted private key]")
	// Output is captured as head and tail: a key cut in the middle leaves
	// its base64 body and END line without the BEGIN line.
	s = privateKeyTail.ReplaceAllString(s, "[redacted private key]")
	s = urlCreds.ReplaceAllString(s, "${1}:***@")
	s = bearer.ReplaceAllString(s, "${1} ***")
	for _, re := range tokenFormats {
		s = re.ReplaceAllString(s, "***")
	}
	s = secretAssign.ReplaceAllStringFunc(s, redactAssign)
	return s
}

var (
	privateKeyBlock = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----.*?(-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----|\z)`)
	privateKeyTail  = regexp.MustCompile(`(?m)(^[A-Za-z0-9+/=]{16,}\r?\n)+-----END [A-Z0-9 ]*PRIVATE KEY( BLOCK)?-----`)
	urlCreds        = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^\s:/@"']*):[^\s@/"']+@`)
	bearer          = regexp.MustCompile(`(?i)\b(bearer|basic|token)[ \t]+[A-Za-z0-9._~+/=-]{8,}`)
	tokenFormats    = []*regexp.Regexp{
		regexp.MustCompile(`\b(ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{30,}`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{30,}`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`),
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\b[0-9]{8,10}:[A-Za-z0-9_-]{35}\b`), // Telegram bot token
		regexp.MustCompile(`\bsmcp_[A-Za-z0-9_-]{16,}`),         // srvmon MCP token
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}`),
	}
	// KEY=value, KEY: value, "key": "value" where the key names a secret.
	secretAssign = regexp.MustCompile(`[A-Za-z0-9_.-]*(?i:pass(?:wd|word|phrase)?|secret|token|api[_-]?key|access[_-]?key|private[_-]?key|credentials?|dsn)[A-Za-z0-9_.-]*"?[ \t]*[:=][ \t]*("[^"\n]*"|'[^'\n]*'|[^\s"',;}\]]+)`)
	assignSep    = regexp.MustCompile(`"?[ \t]*[:=][ \t]*`)
)

// keepValues are values that say how something is configured, not a secret.
var keepValues = set("true", "false", "yes", "no", "on", "off", "null", "none", "nil", "required", "optional",
	"enabled", "disabled", "0", "1", "***", `""`, "''")

func redactAssign(m string) string {
	loc := assignSep.FindStringIndex(m)
	if loc == nil {
		return m
	}
	key, sep, val := m[:loc[0]], m[loc[0]:loc[1]], m[loc[1]:]
	inner := strings.Trim(val, `"'`)
	if keepValues[strings.ToLower(val)] || keepValues[strings.ToLower(inner)] ||
		strings.HasPrefix(inner, "$") || strings.HasPrefix(inner, "[redacted") {
		return m
	}
	q := ""
	if len(val) >= 2 && (val[0] == '"' || val[0] == '\'') {
		q = val[:1]
	}
	return key + sep + q + "***" + q
}
