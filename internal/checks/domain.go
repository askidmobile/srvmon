package checks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// DomainInfo is what is known about a domain registration.
type DomainInfo struct {
	Domain  string     `json:"domain"`
	Expires *time.Time `json:"expires,omitempty"`
	Source  string     `json:"source,omitempty"` // rdap | whois
	Error   string     `json:"error,omitempty"`
	Checked time.Time  `json:"checked_at"`
}

// ErrNoExpiry: the registry answered, but does not publish an expiry date.
var ErrNoExpiry = errors.New("no expiry date")

// DomainLookup finds the expiry of domain registrations over RDAP and falls
// back to WHOIS for zones without RDAP (.ru, .рф, ...).
type DomainLookup struct {
	Client    *http.Client
	Bootstrap string // IANA RDAP bootstrap for DNS
	WhoisIANA string // host:port of the IANA WHOIS server
	Dial      func(ctx context.Context, network, addr string) (net.Conn, error)

	mu       sync.Mutex
	rdap     map[string]string // tld → RDAP base URL
	rdapAt   time.Time
	whoisFor map[string]string // tld → WHOIS server
}

// NewDomainLookup returns a lookup against the public registries.
func NewDomainLookup() *DomainLookup {
	return &DomainLookup{
		Client: &http.Client{
			Timeout: 20 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				TLSClientConfig:     tlsConfig(""),
				TLSHandshakeTimeout: 10 * time.Second,
				ForceAttemptHTTP2:   true,
				MaxIdleConnsPerHost: 2,
				IdleConnTimeout:     time.Minute,
			},
		},
		Bootstrap: "https://data.iana.org/rdap/dns.json",
		WhoisIANA: "whois.iana.org:43",
		Dial:      (&net.Dialer{Timeout: 10 * time.Second}).DialContext,
		whoisFor:  map[string]string{},
	}
}

// Expiry returns when the registration of domain expires and where that came
// from.
func (l *DomainLookup) Expiry(ctx context.Context, domain string) (time.Time, string, error) {
	tld := domain[strings.LastIndexByte(domain, '.')+1:]
	base, err := l.rdapBase(ctx, tld)
	if err == nil && base != "" {
		t, err := l.rdapExpiry(ctx, base, domain)
		if err == nil {
			return t, "rdap", nil
		}
		if errors.Is(err, ErrNoExpiry) {
			// Some registries hide the date in RDAP but show it in WHOIS.
			if t, werr := l.whoisExpiry(ctx, tld, domain); werr == nil {
				return t, "whois", nil
			}
			return time.Time{}, "rdap", err
		}
		// RDAP of the registry is down: try WHOIS before giving up.
		if t, werr := l.whoisExpiry(ctx, tld, domain); werr == nil {
			return t, "whois", nil
		}
		return time.Time{}, "rdap", err
	}
	t, werr := l.whoisExpiry(ctx, tld, domain)
	return t, "whois", werr
}

func (l *DomainLookup) rdapBase(ctx context.Context, tld string) (string, error) {
	l.mu.Lock()
	fresh := l.rdap != nil && time.Since(l.rdapAt) < 24*time.Hour
	if fresh {
		b := l.rdap[tld]
		l.mu.Unlock()
		return b, nil
	}
	l.mu.Unlock()
	var doc struct {
		Services [][][]string `json:"services"`
	}
	if err := l.getJSON(ctx, l.Bootstrap, &doc); err != nil {
		return "", err
	}
	m := map[string]string{}
	for _, s := range doc.Services {
		if len(s) < 2 || len(s[1]) == 0 {
			continue
		}
		// Prefer an https endpoint.
		url := s[1][0]
		for _, u := range s[1] {
			if strings.HasPrefix(u, "https://") {
				url = u
				break
			}
		}
		if !strings.HasSuffix(url, "/") {
			url += "/"
		}
		for _, t := range s[0] {
			m[strings.ToLower(t)] = url
		}
	}
	l.mu.Lock()
	l.rdap, l.rdapAt = m, time.Now()
	l.mu.Unlock()
	return m[tld], nil
}

func (l *DomainLookup) rdapExpiry(ctx context.Context, base, domain string) (time.Time, error) {
	var doc struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := l.getJSON(ctx, base+"domain/"+domain, &doc); err != nil {
		return time.Time{}, err
	}
	for _, e := range doc.Events {
		if strings.EqualFold(e.Action, "expiration") {
			if t, ok := parseDate(e.Date); ok {
				return t, nil
			}
		}
	}
	return time.Time{}, ErrNoExpiry
}

func (l *DomainLookup) getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	req.Header.Set("User-Agent", userAgent)
	resp, err := l.Client.Do(req)
	if err != nil {
		return errors.New(describe(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errors.New(i18n.T("domain not found in the registry"))
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("RDAP: HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 2<<20)).Decode(v)
}

// --- WHOIS ---

func (l *DomainLookup) whoisServer(ctx context.Context, tld string) (string, error) {
	l.mu.Lock()
	srv, ok := l.whoisFor[tld]
	l.mu.Unlock()
	if ok {
		return srv, nil
	}
	text, err := l.whois(ctx, l.WhoisIANA, tld)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(text, "\n") {
		k, v, ok := strings.Cut(line, ":")
		if ok && strings.EqualFold(strings.TrimSpace(k), "whois") {
			srv = strings.TrimSpace(v)
			break
		}
	}
	if srv == "" {
		return "", errors.New(i18n.T("the zone has neither RDAP nor WHOIS"))
	}
	l.mu.Lock()
	l.whoisFor[tld] = srv
	l.mu.Unlock()
	return srv, nil
}

func (l *DomainLookup) whoisExpiry(ctx context.Context, tld, domain string) (time.Time, error) {
	srv, err := l.whoisServer(ctx, tld)
	if err != nil {
		return time.Time{}, err
	}
	if !strings.Contains(srv, ":") {
		srv += ":43"
	}
	text, err := l.whois(ctx, srv, domain)
	if err != nil {
		return time.Time{}, err
	}
	return whoisExpiryFrom(text)
}

func (l *DomainLookup) whois(ctx context.Context, addr, query string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := l.Dial(ctx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("WHOIS: %s", describe(err))
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := io.WriteString(conn, query+"\r\n"); err != nil {
		return "", fmt.Errorf("WHOIS: %s", describe(err))
	}
	b, err := io.ReadAll(io.LimitReader(conn, 256<<10))
	if err != nil && len(b) == 0 {
		return "", fmt.Errorf("WHOIS: %s", describe(err))
	}
	return string(b), nil
}

// whoisKeys are the expiry fields of common WHOIS formats, in order of
// preference.
var whoisKeys = []string{
	"registry expiry date",
	"registrar registration expiration date",
	"expiration date",
	"expiry date",
	"expire date",
	"paid-till", // .ru, .su, .рф
	"free-date",
	"expires",
	"expires on",
	"expire",
	"renewal date",
	"valid until",
}

var notFound = regexp.MustCompile(`(?i)^(no match|not found|no entries found|no data found|status:\s*free|no object found)`)

func whoisExpiryFrom(text string) (time.Time, error) {
	found := map[string]string{}
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if notFound.MatchString(line) {
			return time.Time{}, errors.New(i18n.T("domain not found in the registry"))
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k = strings.ToLower(strings.TrimSpace(k))
		v = strings.TrimSpace(v)
		if _, seen := found[k]; !seen && v != "" {
			found[k] = v
		}
	}
	for _, k := range whoisKeys {
		if v, ok := found[k]; ok {
			if t, ok := parseDate(v); ok {
				return t, nil
			}
		}
	}
	return time.Time{}, ErrNoExpiry
}

var dateLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05 MST",
	"2006-01-02 15:04:05",
	"2006-01-02",
	"2006.01.02",
	"02-Jan-2006",
	"02.01.2006",
	"2006/01/02",
	"January 2 2006",
	"Mon Jan 2 15:04:05 MST 2006",
}

func parseDate(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	// "2026-03-01 (YYYY-MM-DD)", "2026-03-01T00:00:00Z [extra]"
	if i := strings.IndexAny(s, "(["); i > 0 {
		s = strings.TrimSpace(s[:i])
	}
	for _, l := range dateLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
