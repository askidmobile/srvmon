package checks

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/store"
)

func TestParse(t *testing.T) {
	ok := []struct {
		in, raw, kind, host, domain string
		tls                         bool
	}{
		{"https://Example.COM", "https://example.com/", KindHTTP, "example.com", "example.com", true},
		{" http://10.0.0.5:8080/health ", "http://10.0.0.5:8080/health", KindHTTP, "10.0.0.5", "", false},
		{"https://api.shop.example.co.uk/x?y=1#frag", "https://api.shop.example.co.uk/x?y=1", KindHTTP, "api.shop.example.co.uk", "example.co.uk", true},
		{"db.example.org:5432", "db.example.org:5432", KindTCP, "db.example.org", "example.org", false},
		{"tcp://db.example.org:5432", "db.example.org:5432", KindTCP, "db.example.org", "example.org", false},
		{"tls://Mail.Example.org:993", "tls://mail.example.org:993", KindTCP, "mail.example.org", "example.org", true},
		{"[2001:db8::1]:443", "[2001:db8::1]:443", KindTCP, "2001:db8::1", "", false},
		{"redis:6379", "redis:6379", KindTCP, "redis", "", false},
		{"https://my.github.io/", "https://my.github.io/", KindHTTP, "my.github.io", "", true}, // private suffix
		{"https://nas.local/", "https://nas.local/", KindHTTP, "nas.local", "", true},          // not a registry
		{"https://Пример.рф/", "https://xn--e1afmkfd.xn--p1ai/", KindHTTP, "xn--e1afmkfd.xn--p1ai", "xn--e1afmkfd.xn--p1ai", true},
	}
	for _, c := range ok {
		got, err := Parse(c.in)
		if err != nil {
			t.Errorf("%q: %v", c.in, err)
			continue
		}
		if got.Raw != c.raw || got.Kind != c.kind || got.Host != c.host || got.TLS != c.tls || got.Domain() != c.domain {
			t.Errorf("%q: got %+v domain %q", c.in, got, got.Domain())
		}
	}
	for _, in := range []string{"", "example.com", "ftp://x.org", "https://", "x.org:0", "x.org:70000", "https://u:p@x.org/",
		"https://x.org:99999/", "a b:80", "tls://x.org", strings.Repeat("a", 600) + ":80"} {
		if _, err := Parse(in); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestProbeHTTP(t *testing.T) {
	var code atomic.Int32
	code.Store(200)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redir" {
			http.Redirect(w, r, "/", http.StatusFound)
			return
		}
		w.WriteHeader(int(code.Load()))
	}))
	defer srv.Close()
	tg, _ := Parse(srv.URL + "/redir")
	r := probe(context.Background(), tg)
	if !r.Up || r.Status != 200 || r.Cert != nil {
		t.Fatalf("up: %+v", r)
	}
	code.Store(503)
	if r := probe(context.Background(), tg); r.Up || r.Status != 503 || r.Error != "HTTP 503" {
		t.Fatalf("503: %+v", r)
	}
	addr := srv.Listener.Addr().String()
	srv.Close()
	tg, _ = Parse("http://" + addr + "/")
	if r := probe(context.Background(), tg); r.Up || r.Error == "" {
		t.Fatalf("closed: %+v", r)
	}
}

func TestProbeTLSCert(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	// The test certificate is not trusted: the check is down, but the
	// certificate it failed on is still reported.
	tg, _ := Parse(srv.URL)
	r := probe(context.Background(), tg)
	if r.Up || r.Cert == nil || r.Cert.NotAfter.IsZero() || !strings.Contains(r.Error, "certificate") {
		t.Fatalf("https: %+v", r)
	}
	tg, _ = Parse("tls://" + srv.Listener.Addr().String())
	r = probe(context.Background(), tg)
	if r.Up || r.Cert == nil {
		t.Fatalf("tls: %+v", r)
	}
	tg, _ = Parse(srv.Listener.Addr().String())
	if r := probe(context.Background(), tg); !r.Up || r.Cert != nil {
		t.Fatalf("tcp: %+v", r)
	}
}

func TestInvalidCertIsTheLeaf(t *testing.T) {
	leaf := &x509.Certificate{NotAfter: time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)}
	root := &x509.Certificate{NotAfter: time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)}
	err := &tls.CertificateVerificationError{UnverifiedCertificates: []*x509.Certificate{leaf, root},
		Err: x509.UnknownAuthorityError{Cert: root}}
	if c := invalidCert(fmt.Errorf("get: %w", err)); c == nil || !c.NotAfter.Equal(leaf.NotAfter) {
		t.Fatalf("cert: %+v", c)
	}
}

func TestDescribe(t *testing.T) {
	ce := x509.CertificateInvalidError{Reason: x509.Expired, Cert: &x509.Certificate{NotAfter: time.Unix(0, 0)}}
	if got := describe(fmt.Errorf("Get: %w", ce)); got != "certificate expired" {
		t.Errorf("expired: %q", got)
	}
	if c := invalidCert(fmt.Errorf("x: %w", ce)); c == nil || !c.NotAfter.Equal(time.Unix(0, 0).UTC()) {
		t.Errorf("cert of the error: %+v", c)
	}
	if got := describe(&net.DNSError{IsNotFound: true}); got != "host not found" {
		t.Errorf("dns: %q", got)
	}
	if got := describe(errors.New("dial tcp 1.2.3.4:80: connect: connection refused")); got != "connection refused" {
		t.Errorf("refused: %q", got)
	}
	if got := describe(context.DeadlineExceeded); got != "timeout" {
		t.Errorf("timeout: %q", got)
	}
}

func TestWhoisExpiry(t *testing.T) {
	cases := map[string]string{
		"domain: EXAMPLE.RU\nstate: REGISTERED, DELEGATED\npaid-till: 2027-03-01T21:00:00Z\nfree-date: 2027-04-02\n": "2027-03-01T21:00:00Z",
		"   Registry Expiry Date: 2028-08-13T04:00:00Z\n   Registrar Registration Expiration Date: 2028-08-14\n":     "2028-08-13T04:00:00Z",
		"Expiry date: 05-Jan-2029\n":                         "2029-01-05T00:00:00Z",
		"expires: 2027.12.31\n":                              "2027-12-31T00:00:00Z",
		"Expiration Date: 2030-02-01 (YYYY-MM-DD)\n":         "2030-02-01T00:00:00Z",
		"Expiration date: 2026-11-30 12:00:00+01:00\nx: y\n": "2026-11-30T11:00:00Z",
	}
	for text, want := range cases {
		got, err := whoisExpiryFrom(text)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("%q: %v %v, want %s", text, got, err, want)
		}
	}
	if _, err := whoisExpiryFrom("No match for \"NOPE.COM\".\n"); err == nil || errors.Is(err, ErrNoExpiry) {
		t.Errorf("not found: %v", err)
	}
	if _, err := whoisExpiryFrom("Domain: x.de\nStatus: connect\n"); !errors.Is(err, ErrNoExpiry) {
		t.Errorf("no date: %v", err)
	}
}

// fakeWhois answers queries from a map on a local port.
func fakeWhois(t *testing.T, answers map[string]string) string {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer c.Close()
				buf := make([]byte, 256)
				n, _ := c.Read(buf)
				q := strings.TrimSpace(string(buf[:n]))
				_, _ = io.WriteString(c, answers[q])
			}()
		}
	}()
	return ln.Addr().String()
}

func TestDomainLookup(t *testing.T) {
	var whoisAddr string
	rdap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/dns.json":
			fmt.Fprintf(w, `{"services":[[["com","net"],["http://%s/rdap/"]],[["org"],["http://%s/broken/"]]]}`, r.Host, r.Host)
		case "/rdap/domain/example.com":
			_, _ = io.WriteString(w, `{"events":[{"eventAction":"registration","eventDate":"1995-08-14T04:00:00Z"},
				{"eventAction":"expiration","eventDate":"2027-08-13T04:00:00Z"}]}`)
		case "/rdap/domain/missing.com":
			http.NotFound(w, r)
		default:
			http.Error(w, "down", http.StatusBadGateway)
		}
	}))
	defer rdap.Close()
	l := NewDomainLookup()
	l.Client = rdap.Client()
	l.Bootstrap = rdap.URL + "/dns.json"
	l.Dial = func(ctx context.Context, network, addr string) (net.Conn, error) {
		// Every WHOIS server is the fake one.
		return (&net.Dialer{}).DialContext(ctx, network, whoisAddr)
	}
	whoisAddr = fakeWhois(t, map[string]string{
		"ru":          "domain: RU\nwhois: whois.tcinet.ru\n",
		"org":         "domain: ORG\nwhois: whois.publicinterestregistry.org\n",
		"example.ru":  "domain: EXAMPLE.RU\npaid-till: 2027-03-01T21:00:00Z\n",
		"example.org": "Registry Expiry Date: 2029-08-30T04:00:00Z\n",
	})
	ctx := context.Background()
	exp, src, err := l.Expiry(ctx, "example.com")
	if err != nil || src != "rdap" || exp.Format("2006-01-02") != "2027-08-13" {
		t.Fatalf("rdap: %v %s %v", exp, src, err)
	}
	if _, _, err := l.Expiry(ctx, "missing.com"); err == nil {
		t.Fatal("missing domain has an expiry")
	}
	exp, src, err = l.Expiry(ctx, "example.ru")
	if err != nil || src != "whois" || exp.Format("2006-01-02") != "2027-03-01" {
		t.Fatalf("whois: %v %s %v", exp, src, err)
	}
	// The registry's RDAP fails: WHOIS answers.
	exp, src, err = l.Expiry(ctx, "example.org")
	if err != nil || src != "whois" || exp.Year() != 2029 {
		t.Fatalf("rdap down → whois: %v %s %v", exp, src, err)
	}
}

type fakeLookup struct {
	calls atomic.Int32
	err   error
}

func (f *fakeLookup) Expiry(ctx context.Context, domain string) (time.Time, string, error) {
	f.calls.Add(1)
	if f.err != nil {
		return time.Time{}, "rdap", f.err
	}
	return time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC), "rdap", nil
}

func TestRunner(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	id, err := st.CreateServer(ctx, store.Server{Name: "web-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetServerChecks(ctx, id, []string{"https://shop.example.com/", "db.example.com:5432"}); err != nil {
		t.Fatal(err)
	}
	r := New(st, nil, nil)
	r.retry = time.Millisecond
	lk := &fakeLookup{}
	r.lookup = lk
	if s := r.State(id); s.Ready || len(s.Checks) != 0 {
		t.Fatalf("before the first run: %+v", s)
	}
	var mu sync.Mutex
	attempts := map[string]int{}
	exp := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	down := true
	r.probe = func(ctx context.Context, tg Target) Result {
		mu.Lock()
		defer mu.Unlock()
		attempts[tg.Raw]++
		if tg.Kind == KindTCP && down {
			return Result{Error: "connection refused", Checked: time.Now()}
		}
		res := Result{Up: true, Checked: time.Now()}
		if tg.TLS && attempts[tg.Raw] == 1 {
			res.Cert = &CertInfo{NotAfter: exp}
		}
		return res
	}
	r.RunOnce(ctx)
	s := r.State(id)
	if !s.Ready || len(s.Checks) != 2 || !s.Checks[0].Result.Up || s.Checks[1].Result.Up {
		t.Fatalf("state: %+v", s)
	}
	if attempts["db.example.com:5432"] != 2 {
		t.Errorf("a failed check is retried once: %d attempts", attempts["db.example.com:5432"])
	}
	since := s.Checks[1].Result.DownSince
	if since == nil {
		t.Fatal("no down_since")
	}
	if total, d := r.Summary(id); total != 2 || d != 1 {
		t.Errorf("summary %d/%d", total, d)
	}
	r.RunOnce(ctx)
	s = r.State(id)
	if got := s.Checks[1].Result.DownSince; got == nil || !got.Equal(*since) {
		t.Errorf("down_since moved: %v → %v", since, got)
	}
	// The second run had no certificate: the last one is kept.
	if c := s.Checks[0].Result.Cert; c == nil || !c.NotAfter.Equal(exp) {
		t.Errorf("certificate lost: %+v", c)
	}
	down = false
	r.RunOnce(ctx)
	if s := r.State(id); !s.Checks[1].Result.Up || s.Checks[1].Result.DownSince != nil {
		t.Errorf("recovered: %+v", s.Checks[1].Result)
	}

	// Domains: one registrable domain for both targets.
	if s := r.State(id); s.DomainsReady {
		t.Fatal("domains ready before a lookup")
	}
	r.LookupDomains(ctx)
	s = r.State(id)
	if !s.DomainsReady || len(s.Domains) != 1 || s.Domains[0].Domain != "example.com" || s.Domains[0].Expires == nil {
		t.Fatalf("domains: %+v", s.Domains)
	}
	r.LookupDomains(ctx) // not due yet
	if n := lk.calls.Load(); n != 1 {
		t.Errorf("lookups: %d", n)
	}
	// A failed refresh keeps the known date.
	r.domains["example.com"].next = time.Time{}
	lk.err = errors.New("registry down")
	r.LookupDomains(ctx)
	if d := r.State(id).Domains[0]; d.Expires == nil || d.Error == "" {
		t.Errorf("after a failed refresh: %+v", d)
	}

	// Removing a target drops it and its result at once.
	if err := st.SetServerChecks(ctx, id, []string{"db.example.com:5432"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if s := r.State(id); len(s.Checks) != 1 || !s.Ready || len(s.Domains) != 1 {
		t.Errorf("after removal: %+v", s)
	}
	if err := st.DeleteServer(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	if total, _ := r.Summary(id); total != 0 || len(r.results) != 0 {
		t.Errorf("deleted server still has checks: %d %d", total, len(r.results))
	}
}

func TestRetryKeepsFirstCertificate(t *testing.T) {
	r := New(nil, nil, nil)
	r.retry = time.Millisecond
	exp := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	n := 0
	r.probe = func(ctx context.Context, tg Target) Result {
		n++
		if n == 1 {
			return Result{Error: "certificate expired", Cert: &CertInfo{NotAfter: exp}}
		}
		return Result{Error: "timeout"}
	}
	res := r.checkOne(context.Background(), store.Check{Target: "https://x.example.com/"})
	if res.Error != "timeout" || res.Cert == nil || !res.Cert.NotAfter.Equal(exp) {
		t.Fatalf("%+v", res)
	}
}

func TestTLSConfigClassicCurves(t *testing.T) {
	for _, c := range tlsConfig("").CurvePreferences {
		if c == tls.X25519MLKEM768 {
			t.Fatal("post-quantum key share offered")
		}
	}
}
