package alerts

import (
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/checks"
	"github.com/askidmobile/srvmon/internal/store"
)

type fakeChecks struct{ st checks.ServerState }

func (f *fakeChecks) State(int64) checks.ServerState { return f.st }

func TestCheckDownFiresAfterDurationAndResolves(t *testing.T) {
	te := newTestEnv(t)
	fc := &fakeChecks{}
	te.e.SetChecks(fc)
	down := &checks.Result{Error: "timeout"}
	fc.st = checks.ServerState{Ready: false, Checks: []checks.CheckState{{Target: "https://shop.example.com/"}}}
	te.eval(t) // no results yet: nothing decided
	fc.st = checks.ServerState{Ready: true, DomainsReady: true, Checks: []checks.CheckState{
		{Target: "https://shop.example.com/", Result: down},
		{Target: "db.example.com:5432", Result: &checks.Result{Up: true}},
	}}
	te.eval(t)
	if n := len(te.open(t)); n != 0 {
		t.Fatalf("fired before the duration: %d", n)
	}
	te.now = te.now.Add(te.rule(t, store.KindCheckDown).Duration() + time.Second)
	te.eval(t)
	open := te.open(t)
	if len(open) != 1 || open[0].Subject != "https://shop.example.com/" || open[0].Severity != store.SevCrit ||
		open[0].Message != "https://shop.example.com/ is down: timeout" {
		t.Fatalf("open: %+v", open)
	}
	// A restart of the panel (no results yet) keeps the alert.
	fc.st.Ready = false
	te.eval(t)
	if len(te.open(t)) != 1 {
		t.Fatal("resolved without data")
	}
	fc.st.Ready = true
	fc.st.Checks[0].Result = &checks.Result{Up: true}
	te.eval(t)
	if len(te.open(t)) != 0 {
		t.Fatal("not resolved")
	}
	if got := te.n.list(); len(got) != 2 || !got[1].Resolved {
		t.Fatalf("notifications: %+v", got)
	}
}

func TestCertAndDomainExpiry(t *testing.T) {
	te := newTestEnv(t)
	fc := &fakeChecks{}
	te.e.SetChecks(fc)
	soon := te.now.Add(10*24*time.Hour + time.Hour)
	later := te.now.Add(200 * 24 * time.Hour)
	domain := te.now.Add(20*24*time.Hour + time.Hour)
	fc.st = checks.ServerState{Ready: true, DomainsReady: true,
		Checks: []checks.CheckState{
			{Target: "https://a.example.com/", TLS: true, Result: &checks.Result{Up: true, Cert: &checks.CertInfo{NotAfter: soon}}},
			{Target: "https://b.example.com/", TLS: true, Result: &checks.Result{Up: true, Cert: &checks.CertInfo{NotAfter: later}}},
			{Target: "db.example.com:5432", Result: &checks.Result{Up: true}},
		},
		Domains: []checks.DomainInfo{{Domain: "example.com", Expires: &domain}, {Domain: "unknown.org"}},
	}
	te.eval(t)
	got := map[string]string{}
	for _, a := range te.open(t) {
		got[a.Kind+" "+a.Subject] = a.Message
	}
	if len(got) != 2 ||
		got["cert_expiry https://a.example.com/"] != "certificate of https://a.example.com/ expires in 10 days ("+soon.UTC().Format("02.01.2006")+")" ||
		got["domain_expiry example.com"] != "domain example.com expires in 20 days ("+domain.UTC().Format("02.01.2006")+")" {
		t.Fatalf("open: %v", got)
	}
	// After a restart the certificate is not seen yet (timeout) and the
	// domain lookup failed: open alerts stay, nothing resolves.
	before := len(te.n.list())
	fc.st.Checks[0].Result = &checks.Result{Error: "timeout"}
	fc.st.Domains[0].Expires = nil
	te.eval(t)
	if open := te.open(t); len(open) != 2 || len(te.n.list()) != before {
		t.Fatalf("unknown expiry resolved alerts: %+v", open)
	}
	fc.st.Domains[0].Expires = &domain
	// The certificate was renewed.
	fc.st.Checks[0].Result.Cert = &checks.CertInfo{NotAfter: later}
	te.eval(t)
	if open := te.open(t); len(open) != 1 || open[0].Kind != store.KindDomainExpiry {
		t.Fatalf("after renewal: %+v", open)
	}
}

func TestExpiryTexts(t *testing.T) {
	at := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	if got := CertText("x.org:993", -1, at); got != "certificate of x.org:993 expired 1 day ago (03.11.2026)" {
		t.Error(got)
	}
	if got := DomainText("x.org", 1, at); got != "domain x.org expires in 1 day (03.11.2026)" {
		t.Error(got)
	}
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	if d := DaysLeft(now, at); d != 1 {
		t.Errorf("days left %d", d)
	}
	if d := DaysLeft(now, now.Add(-time.Hour)); d != -1 {
		t.Errorf("passed: %d", d)
	}
}
