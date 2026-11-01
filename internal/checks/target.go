// Package checks runs availability checks from the panel: HTTP(S) URLs and
// TCP ports of a server, the expiry of their TLS certificates and the expiry
// of the domain registrations behind them.
package checks

import (
	"errors"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/net/publicsuffix"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// Target kinds.
const (
	KindHTTP = "http"
	KindTCP  = "tcp"
)

// Target is a parsed check target.
type Target struct {
	Raw  string // as stored, normalized
	Kind string // http | tcp
	URL  string // http: the URL to GET
	Addr string // tcp: host:port
	Host string // host name or IP without brackets
	TLS  bool   // the certificate is checked (https://, tls://)
}

// Domain returns the registrable domain of the target ("example.co.uk" for
// "api.example.co.uk"), or "" for IPs and single-label hosts.
func (t Target) Domain() string {
	h := strings.TrimSuffix(strings.ToLower(t.Host), ".")
	if h == "" || net.ParseIP(h) != nil || !strings.Contains(h, ".") {
		return ""
	}
	ascii, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return ""
	}
	d, err := publicsuffix.EffectiveTLDPlusOne(ascii)
	if err != nil {
		return ""
	}
	// Private suffixes (github.io, workers.dev, ...) are not registrations
	// of the owner; neither are names under an unknown TLD (.local, .lan).
	if suffix, icann := publicsuffix.PublicSuffix(ascii); !icann || suffix == ascii {
		return ""
	}
	return d
}

const maxTargetLen = 512

// Parse validates and normalizes a target:
//
//	https://example.com/health   HTTP GET, certificate checked
//	http://10.0.0.5:8080/        HTTP GET
//	tls://mail.example.com:993   TCP connect + TLS handshake, certificate checked
//	db.example.com:5432          TCP connect (tcp:// prefix optional)
func Parse(s string) (Target, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Target{}, errors.New(i18n.T("empty target"))
	}
	if len(s) > maxTargetLen || !utf8.ValidString(s) || strings.ContainsAny(s, " \t\r\n") {
		return Target{}, errors.New(i18n.T("%s: not a URL or host:port", short(s)))
	}
	lower := strings.ToLower(s)
	switch {
	case strings.HasPrefix(lower, "http://"), strings.HasPrefix(lower, "https://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" || u.User != nil {
			return Target{}, errors.New(i18n.T("%s: not a URL or host:port", short(s)))
		}
		if p := u.Port(); p != "" && !validPort(p) {
			return Target{}, errors.New(i18n.T("%s: port from 1 to 65535", short(s)))
		}
		u.Scheme = strings.ToLower(u.Scheme)
		host, err := asciiHost(u.Hostname())
		if err != nil {
			return Target{}, errors.New(i18n.T("%s: not a URL or host:port", short(s)))
		}
		switch port := u.Port(); {
		case port != "":
			u.Host = net.JoinHostPort(host, port)
		case strings.Contains(host, ":"):
			u.Host = "[" + host + "]"
		default:
			u.Host = host
		}
		u.Fragment = ""
		if u.Path == "" {
			u.Path = "/"
		}
		raw := u.String()
		return Target{Raw: raw, Kind: KindHTTP, URL: raw, Host: u.Hostname(), TLS: u.Scheme == "https"}, nil
	}
	tls := false
	rest := s
	switch {
	case strings.HasPrefix(lower, "tls://"):
		tls, rest = true, s[len("tls://"):]
	case strings.HasPrefix(lower, "tcp://"):
		rest = s[len("tcp://"):]
	case strings.Contains(s, "://"):
		return Target{}, errors.New(i18n.T("%s: use http://, https://, tls:// or host:port", short(s)))
	}
	rest = strings.TrimSuffix(rest, "/")
	host, port, err := net.SplitHostPort(rest)
	if err != nil || host == "" || strings.ContainsAny(host, "/?#@") {
		return Target{}, errors.New(i18n.T("%s: not a URL or host:port", short(s)))
	}
	if !validPort(port) {
		return Target{}, errors.New(i18n.T("%s: port from 1 to 65535", short(s)))
	}
	if host, err = asciiHost(host); err != nil {
		return Target{}, errors.New(i18n.T("%s: not a URL or host:port", short(s)))
	}
	addr := net.JoinHostPort(host, port)
	raw := addr
	if tls {
		raw = "tls://" + addr
	}
	return Target{Raw: raw, Kind: KindTCP, Addr: addr, Host: host, TLS: tls}, nil
}

// asciiHost lowercases a host and converts an internationalized name to
// punycode, so one domain is always written the same way.
func asciiHost(h string) (string, error) {
	h = strings.ToLower(h)
	if h == "" || net.ParseIP(h) != nil {
		return h, nil
	}
	return idna.Lookup.ToASCII(h)
}

func validPort(p string) bool {
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

func short(s string) string {
	if utf8.RuneCountInString(s) <= 60 {
		return s
	}
	r := []rune(s)
	return string(r[:57]) + "…"
}
