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
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// Timeout bounds one attempt of a check.
const Timeout = 10 * time.Second

// Result is the outcome of one check run.
type Result struct {
	Up        bool      `json:"up"`
	LatencyMS int64     `json:"latency_ms"`
	Status    int       `json:"status,omitempty"` // HTTP status code
	Error     string    `json:"error,omitempty"`
	Checked   time.Time `json:"checked_at"`
	// DownSince is when the target went down (kept while it stays down).
	DownSince *time.Time `json:"down_since,omitempty"`
	// Cert is the leaf certificate of a TLS target. It is kept when the
	// certificate itself is invalid (expired, wrong name), so the expiry
	// shows up.
	Cert *CertInfo `json:"cert,omitempty"`
}

// CertInfo describes a TLS certificate.
type CertInfo struct {
	Subject  string    `json:"subject"`
	Issuer   string    `json:"issuer"`
	NotAfter time.Time `json:"not_after"`
}

// tlsConfig offers only classic key exchanges: some networks drop
// ClientHellos with the large post-quantum X25519MLKEM768 share, and a
// check must not report a site down because of the panel's own network.
func tlsConfig(serverName string) *tls.Config {
	return &tls.Config{
		ServerName:       serverName,
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256, tls.CurveP384},
		RootCAs:          rootCAs,
	}
}

// rootCAs replaces the system roots in tests.
var rootCAs *x509.CertPool

const userAgent = "srvmon-check/1"

// probe runs one attempt.
func probe(ctx context.Context, t Target) Result {
	ctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	start := time.Now()
	var r Result
	switch t.Kind {
	case KindHTTP:
		r = probeHTTP(ctx, t)
	default:
		r = probeTCP(ctx, t)
	}
	r.LatencyMS = time.Since(start).Milliseconds()
	r.Checked = time.Now().UTC()
	return r
}

func probeHTTP(ctx context.Context, t Target) Result {
	var first *tls.ConnectionState
	// The certificate of the first verified handshake, which is always with
	// the URL itself: kept for when the server then fails to answer.
	var mu sync.Mutex
	var handshake *CertInfo
	cfg := tlsConfig("")
	cfg.VerifyConnection = func(cs tls.ConnectionState) error {
		mu.Lock()
		defer mu.Unlock()
		if handshake == nil {
			handshake = certOf(&cs)
		}
		return nil
	}
	tr := &http.Transport{
		Proxy:               nil, // the target is checked from the panel's own network
		DialContext:         (&net.Dialer{Timeout: Timeout}).DialContext,
		TLSClientConfig:     cfg,
		TLSHandshakeTimeout: Timeout,
		DisableKeepAlives:   true,
		ForceAttemptHTTP2:   true,
	}
	defer tr.CloseIdleConnections()
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			// The certificate of the URL itself, not of where it redirects.
			if len(via) == 1 && req.Response != nil && req.Response.TLS != nil {
				st := *req.Response.TLS
				first = &st
			}
			if len(via) >= 5 {
				return errors.New(i18n.T("too many redirects"))
			}
			return nil
		},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.URL, nil)
	if err != nil {
		return Result{Error: err.Error()}
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := client.Do(req)
	if err != nil {
		r := Result{Error: describe(err)}
		r.Cert = invalidCert(err)
		mu.Lock()
		if r.Cert == nil {
			r.Cert = handshake
		}
		mu.Unlock()
		if first != nil {
			r.Cert = certOf(first)
		}
		return r
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	resp.Body.Close()
	r := Result{Status: resp.StatusCode, Up: resp.StatusCode < 400}
	if !r.Up {
		r.Error = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if first == nil && resp.TLS != nil && resp.Request != nil && resp.Request.URL.Hostname() == t.Host {
		first = resp.TLS
	}
	if first != nil && t.TLS {
		r.Cert = certOf(first)
	}
	return r
}

func probeTCP(ctx context.Context, t Target) Result {
	d := &net.Dialer{Timeout: Timeout}
	conn, err := d.DialContext(ctx, "tcp", t.Addr)
	if err != nil {
		return Result{Error: describe(err)}
	}
	defer conn.Close()
	if !t.TLS {
		return Result{Up: true}
	}
	tc := tls.Client(conn, tlsConfig(t.Host))
	if err := tc.HandshakeContext(ctx); err != nil {
		return Result{Error: describe(err), Cert: invalidCert(err)}
	}
	st := tc.ConnectionState()
	return Result{Up: true, Cert: certOf(&st)}
}

func certOf(st *tls.ConnectionState) *CertInfo {
	if st == nil || len(st.PeerCertificates) == 0 {
		return nil
	}
	return infoOf(st.PeerCertificates[0])
}

func infoOf(c *x509.Certificate) *CertInfo {
	sub := c.Subject.CommonName
	if sub == "" && len(c.DNSNames) > 0 {
		sub = c.DNSNames[0]
	}
	return &CertInfo{Subject: sub, Issuer: c.Issuer.CommonName, NotAfter: c.NotAfter.UTC()}
}

// invalidCert returns the certificate a verification error is about.
func invalidCert(err error) *CertInfo {
	// The leaf is the first certificate the server sent; the one an x509
	// error names may be an intermediate or the root.
	var ve *tls.CertificateVerificationError
	if errors.As(err, &ve) && len(ve.UnverifiedCertificates) > 0 {
		return infoOf(ve.UnverifiedCertificates[0])
	}
	var ci x509.CertificateInvalidError
	if errors.As(err, &ci) && ci.Cert != nil {
		return infoOf(ci.Cert)
	}
	var hn x509.HostnameError
	if errors.As(err, &hn) && hn.Certificate != nil {
		return infoOf(hn.Certificate)
	}
	var ua x509.UnknownAuthorityError
	if errors.As(err, &ua) && ua.Cert != nil {
		return infoOf(ua.Cert)
	}
	return nil
}

// describe shortens network errors for alerts: the URL and the operation are
// already known.
func describe(err error) string {
	var ci x509.CertificateInvalidError
	var hn x509.HostnameError
	var ua x509.UnknownAuthorityError
	switch {
	case errors.As(err, &ci) && ci.Reason == x509.Expired:
		return i18n.T("certificate expired")
	case errors.As(err, &ci), errors.As(err, &hn), errors.As(err, &ua):
		return i18n.T("invalid certificate: %s", lastPart(err.Error()))
	case errors.Is(err, context.DeadlineExceeded):
		return i18n.T("timeout")
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return i18n.T("timeout")
	}
	var de *net.DNSError
	if errors.As(err, &de) {
		if de.IsNotFound {
			return i18n.T("host not found")
		}
		return i18n.T("dns error")
	}
	msg := err.Error()
	switch {
	case strings.Contains(msg, "connection refused"):
		return i18n.T("connection refused")
	case strings.Contains(msg, "no route to host"), strings.Contains(msg, "network is unreachable"):
		return i18n.T("host unreachable")
	case strings.Contains(msg, "connection reset"):
		return i18n.T("connection reset")
	}
	return lastPart(msg)
}

func lastPart(msg string) string {
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		msg = msg[i+2:]
	}
	if len(msg) > 200 {
		msg = msg[:200]
	}
	return msg
}
