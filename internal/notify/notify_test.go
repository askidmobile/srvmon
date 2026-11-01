package notify

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

type sent struct{ url, title, body string }

type fakeSender struct {
	mu   sync.Mutex
	sent []sent
	fail int // fail this many sends first
	ch   chan struct{}
}

func (f *fakeSender) Check(raw string) error {
	if strings.HasPrefix(raw, "bogus://") {
		return errors.New("unknown service: \"bogus\"")
	}
	return nil
}

func (f *fakeSender) Send(_ context.Context, raw, title, body string) error {
	f.mu.Lock()
	defer func() {
		f.mu.Unlock()
		if f.ch != nil {
			f.ch <- struct{}{}
		}
	}()
	if f.fail > 0 {
		f.fail--
		return errors.New("post " + raw + ": connection refused")
	}
	f.sent = append(f.sent, sent{raw, title, body})
	return nil
}

func (f *fakeSender) list() []sent {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sent(nil), f.sent...)
}

type fakeTG struct {
	notified []alerts.Notification
	texts    []string
	ok       bool
}

func (f *fakeTG) Notify(n alerts.Notification) bool { f.notified = append(f.notified, n); return f.ok }
func (f *fakeTG) Send(s string) bool                { f.texts = append(f.texts, s); return f.ok }

func newHub(t *testing.T, box *secret.Box, st *store.Store, fs *fakeSender) *Hub {
	t.Helper()
	h, err := New(context.Background(), Options{Store: st, Secrets: box, Sender: fs, PanelURL: "https://mon.example.com/"})
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func setup(t *testing.T) (*Hub, *fakeSender, *store.Store, *secret.Box) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	fs := &fakeSender{ch: make(chan struct{}, 100)}
	return newHub(t, box, st, fs), fs, st, box
}

func ptr(s string) *string { return &s }

func run(t *testing.T, h *Hub) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go h.Run(ctx)
}

func wait(t *testing.T, fs *fakeSender, n int) {
	t.Helper()
	for range n {
		select {
		case <-fs.ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timeout waiting for a send")
		}
	}
}

func TestChannelsPersistSealed(t *testing.T) {
	h, fs, st, box := setup(t)
	ctx := context.Background()
	url := "ntfy://ntfy.sh/very-secret-topic"
	v, err := h.Add(ctx, Input{Name: "phone", URL: ptr(url), MinSeverity: "warn", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if v.Service != "ntfy" || v.Target != "ntfy.sh" {
		t.Fatalf("view = %+v", v)
	}
	raw, _ := st.GetSetting(ctx, settingKey)
	if strings.Contains(raw, "secret") {
		t.Fatal("channel URL stored in clear text")
	}
	h2 := newHub(t, box, st, fs)
	if l := h2.List(); len(l) != 1 || l[0].Name != "phone" || l[0].MinSeverity != "warn" {
		t.Fatalf("reloaded = %+v", l)
	}
	if c, _ := h2.channel(v.ID); c.URL != url {
		t.Fatalf("url = %q", c.URL)
	}
	// Update without URL keeps it; delete removes it.
	if _, err := h2.Update(ctx, v.ID, Input{Name: "phone 2", MinSeverity: "crit", Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if c, _ := h2.channel(v.ID); c.URL != url || c.Name != "phone 2" || c.Enabled {
		t.Fatalf("updated = %+v", c)
	}
	if err := h2.Delete(ctx, v.ID); err != nil {
		t.Fatal(err)
	}
	if err := h2.Delete(ctx, v.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestValidation(t *testing.T) {
	h, _, _, _ := setup(t)
	ctx := context.Background()
	var ve *ValidationError
	for _, in := range []Input{
		{Name: "", URL: ptr("ntfy://ntfy.sh/x"), MinSeverity: "warn"},
		{Name: "x", URL: ptr("ntfy://ntfy.sh/x"), MinSeverity: "loud"},
		{Name: "x", URL: ptr(""), MinSeverity: "warn"},
		{Name: "x", URL: ptr("bogus://x"), MinSeverity: "warn"},
	} {
		if _, err := h.Add(ctx, in); !errors.As(err, &ve) {
			t.Errorf("%+v: err = %v", in, err)
		}
	}
	noBox := &Hub{sender: &fakeSender{}}
	if _, err := noBox.Add(ctx, Input{Name: "x", URL: ptr("ntfy://a/b"), MinSeverity: "warn"}); !errors.Is(err, ErrNoSecrets) {
		t.Fatalf("no master key: %v", err)
	}
}

func TestNotifyFiltersBySeverity(t *testing.T) {
	h, fs, _, _ := setup(t)
	ctx := context.Background()
	crit, _ := h.Add(ctx, Input{Name: "pager", URL: ptr("ntfy://ntfy.sh/crit"), MinSeverity: "crit", Enabled: true})
	_, _ = h.Add(ctx, Input{Name: "mail", URL: ptr("ntfy://ntfy.sh/all"), MinSeverity: "info", Enabled: true})
	_, _ = h.Add(ctx, Input{Name: "off", URL: ptr("ntfy://ntfy.sh/off"), MinSeverity: "info", Enabled: false})
	tg := &fakeTG{ok: true}
	h.SetTelegram(tg)
	run(t, h)

	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	warn := alerts.Notification{Alert: store.Alert{ID: 7, ServerName: "web-1", Severity: "warn", Message: "cpu 95%", StartedAt: now}}
	if !h.Notify(warn) {
		t.Fatal("warn not accepted")
	}
	wait(t, fs, 1)
	got := fs.list()
	if len(got) != 1 || got[0].url != "ntfy://ntfy.sh/all" {
		t.Fatalf("warn went to %+v", got)
	}
	if got[0].title != "🟡 WARN · web-1" || !strings.Contains(got[0].body, "cpu 95%\n#7 · 01.11 13:00") ||
		!strings.HasSuffix(got[0].body, "\nhttps://mon.example.com/#/alerts") {
		t.Fatalf("message = %+v", got[0])
	}
	if len(tg.notified) != 1 {
		t.Fatal("telegram not notified")
	}

	end := now.Add(90 * time.Second)
	res := alerts.Notification{Alert: store.Alert{ID: 8, Severity: "crit", Message: "agent back", StartedAt: now, ResolvedAt: &end}, Resolved: true}
	h.Notify(res)
	wait(t, fs, 2)
	if got := fs.list(); len(got) != 3 || !strings.HasPrefix(got[1].title+got[2].title, "✅ RESOLVED · srvmon") {
		t.Fatalf("resolved = %+v", got)
	}
	// The fake signals inside Send; the hub records the status after Send
	// returns, so it may land a moment later.
	deadline := time.Now().Add(5 * time.Second)
	for {
		v := h.List()
		if v[0].ID == crit.ID && v[0].LastSent != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("status not recorded: %+v", v[0])
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestNotifyAcceptance(t *testing.T) {
	h, fs, _, _ := setup(t)
	n := alerts.Notification{Alert: store.Alert{ID: 1, Severity: "info", Message: "login"}}
	if h.Notify(n) {
		t.Fatal("accepted with no destinations")
	}
	tg := &fakeTG{ok: false}
	h.SetTelegram(tg)
	if h.Notify(n) {
		t.Fatal("accepted while Telegram is not configured")
	}
	_, _ = h.Add(context.Background(), Input{Name: "mail", URL: ptr("ntfy://ntfy.sh/x"), MinSeverity: "warn", Enabled: true})
	if h.Notify(n) {
		t.Fatal("info accepted by a warn+ channel")
	}
	n.Alert.Severity = "warn"
	if !h.Notify(n) {
		t.Fatal("warn not accepted by the channel")
	}
	_ = fs
}

func TestRetryAndRedactedError(t *testing.T) {
	old := retryWait
	retryWait = func(int) time.Duration { return time.Millisecond }
	t.Cleanup(func() { retryWait = old })
	h, fs, _, _ := setup(t)
	fs.fail = 5
	url := "gotify://g.example.com/AbcdefSecretToken"
	v, _ := h.Add(context.Background(), Input{Name: "g", URL: ptr(url), MinSeverity: "info", Enabled: true})
	run(t, h)
	h.Enqueue(Message{Severity: "crit", Title: "t", Body: "b"})
	wait(t, fs, attempts)
	time.Sleep(20 * time.Millisecond)
	l := h.List()
	if l[0].LastError == "" || strings.Contains(l[0].LastError, "Secret") {
		t.Fatalf("last error = %q", l[0].LastError)
	}
	if en, failing := h.Health(); en != 1 || failing != 1 {
		t.Fatalf("health = %d %d", en, failing)
	}
	// The next message succeeds after two failures and clears the error.
	fs.fail = 2
	h.Enqueue(Message{Severity: "crit", Title: "t", Body: "b"})
	wait(t, fs, 3)
	time.Sleep(20 * time.Millisecond)
	if l := h.List(); l[0].LastError != "" || l[0].LastSent == nil || l[0].ID != v.ID {
		t.Fatalf("after success = %+v", l[0])
	}
}

func TestSendTextAndTest(t *testing.T) {
	h, fs, _, _ := setup(t)
	tg := &fakeTG{ok: true}
	h.SetTelegram(tg)
	v, _ := h.Add(context.Background(), Input{Name: "mail", URL: ptr("ntfy://ntfy.sh/x"), MinSeverity: "crit", Enabled: true})
	run(t, h)
	if err := h.SendText(context.Background(), "⚠️ <b>task</b> &lt;disk&gt;\nALERT: full"); err != nil {
		t.Fatal(err)
	}
	wait(t, fs, 1)
	got := fs.list()[0]
	if got.title != "srvmon · ⚠️ task <disk>" || !strings.HasPrefix(got.body, "⚠️ task <disk>\nALERT: full") {
		t.Fatalf("text = %+v", got)
	}
	if len(tg.texts) != 1 || !strings.Contains(tg.texts[0], "<b>") {
		t.Fatalf("telegram = %v", tg.texts)
	}
	if err := h.Test(context.Background(), v.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.Test(context.Background(), 99); !errors.Is(err, ErrNotFound) {
		t.Fatalf("test unknown: %v", err)
	}
}

func TestDescribe(t *testing.T) {
	for raw, want := range map[string]string{
		"smtp://user:pass@smtp.example.com:587/?from=a@b.c&to=d@e.f": "smtp smtp.example.com:587",
		"discord://token@12345":                          "discord ",
		"pushbullet://o.SecretApiToken":                  "pushbullet ",
		"generic+https://hooks.example.com/abc?x=secret": "generic hooks.example.com",
		"::bad": "? ",
	} {
		s, tgt := Describe(raw)
		if s+" "+tgt != want {
			t.Errorf("%s → %q %q", raw, s, tgt)
		}
	}
}

// A message queued for a deleted channel must not go to a new channel that
// got its id, and a result of an old URL must not show up after a change.
func TestReplacedChannelGetsNoStaleMessages(t *testing.T) {
	h, fs, _, _ := setup(t)
	ctx := context.Background()
	_, _ = h.Add(ctx, Input{Name: "a", URL: ptr("ntfy://ntfy.sh/a"), MinSeverity: "info", Enabled: true})
	old, _ := h.Add(ctx, Input{Name: "b", URL: ptr("ntfy://ntfy.sh/old"), MinSeverity: "info", Enabled: true})
	h.Enqueue(Message{Severity: "info", Title: "t", Body: "b"}) // queued, workers not running yet
	if err := h.Delete(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	repl, _ := h.Add(ctx, Input{Name: "c", URL: ptr("ntfy://ntfy.sh/new"), MinSeverity: "info", Enabled: true})
	if repl.ID != old.ID {
		t.Skip("id not reused; nothing to check")
	}
	run(t, h)
	wait(t, fs, 1)
	time.Sleep(50 * time.Millisecond)
	for _, s := range fs.list() {
		if s.url == "ntfy://ntfy.sh/new" {
			t.Fatal("stale message sent to the replacement channel")
		}
	}
	// A result recorded for the old generation is dropped.
	stale, _ := h.channel(repl.ID)
	stale.Gen--
	stale.URL = "ntfy://ntfy.sh/OldSecretTopic"
	h.record(stale, errors.New("post ntfy://ntfy.sh/OldSecretTopic: refused"))
	if l := h.List(); l[1].LastError != "" {
		t.Fatalf("stale error recorded: %q", l[1].LastError)
	}
}

// Messages to one channel keep their order even when the first one is
// retried: a recovery must not overtake its alert.
func TestChannelOrderSurvivesRetries(t *testing.T) {
	old := retryWait
	retryWait = func(int) time.Duration { return 20 * time.Millisecond }
	t.Cleanup(func() { retryWait = old })
	h, fs, _, _ := setup(t)
	fs.fail = 1
	_, _ = h.Add(context.Background(), Input{Name: "a", URL: ptr("ntfy://ntfy.sh/a"), MinSeverity: "info", Enabled: true})
	run(t, h)
	h.Enqueue(Message{Severity: "crit", Title: "FIRING", Body: "b"})
	h.Enqueue(Message{Severity: "crit", Title: "RESOLVED", Body: "b"})
	wait(t, fs, 3)
	got := fs.list()
	if len(got) != 2 || got[0].title != "FIRING" || got[1].title != "RESOLVED" {
		t.Fatalf("order = %+v", got)
	}
}

// A full channel queue rejects messages instead of accepting and dropping
// them, so the alerts engine retries them later.
func TestFullChannelQueueRejects(t *testing.T) {
	h, _, _, _ := setup(t)
	_, _ = h.Add(context.Background(), Input{Name: "a", URL: ptr("ntfy://ntfy.sh/a"), MinSeverity: "info", Enabled: true})
	// Workers are not running: the queue fills up.
	for i := range queueSize {
		if h.Enqueue(Message{Severity: "crit", Title: "t", Body: "b"}) != 1 {
			t.Fatalf("message %d rejected", i)
		}
	}
	if h.Enqueue(Message{Severity: "crit", Title: "t", Body: "b"}) != 0 {
		t.Fatal("accepted beyond the queue")
	}
	if h.Notify(alerts.Notification{Alert: store.Alert{ID: 1, Severity: "crit"}}) {
		t.Fatal("notification accepted with a full queue")
	}
}

func TestRedactEscapedSecrets(t *testing.T) {
	raw := "generic+https://hooks.example.com/Secret%2BWebhookToken?key=a%2Fb%2Fsecretkey"
	msg := `Post "https://hooks.example.com/Secret%2BWebhookToken?key=a%2Fb%2Fsecretkey": dial tcp: connection refused; Secret+WebhookToken a/b/secretkey`
	got := redact(msg, raw)
	for _, leak := range []string{"Secret", "secretkey"} {
		if strings.Contains(got, leak) {
			t.Fatalf("leaked %q: %s", leak, got)
		}
	}
	if !strings.Contains(got, "hooks.example.com") {
		t.Fatalf("host redacted too: %s", got)
	}
}

func TestRedactShortSecrets(t *testing.T) {
	got := redact(`Post "https://hooks.example.com/abcde?k=xyz": connection refused (token abcde)`, "generic+https://hooks.example.com/abcde?k=xyz")
	if strings.Contains(got, "abcde") || strings.Contains(got, "xyz") || !strings.Contains(got, "https://hooks.example.com/***") {
		t.Fatalf("redacted = %s", got)
	}
	// A URL without secrets stays readable.
	if got := redact("dial https://ntfy.sh: timeout", "ntfy://ntfy.sh/topic-name"); got != "dial https://ntfy.sh: timeout" {
		t.Fatalf("plain url changed: %s", got)
	}
}

func TestDiagnosisFollowUp(t *testing.T) {
	h, fs, _, _ := setup(t)
	_, _ = h.Add(context.Background(), Input{Name: "a", URL: ptr("ntfy://ntfy.sh/a"), MinSeverity: "crit", Enabled: true})
	tg := &fakeTG{ok: true}
	h.SetTelegram(tg)
	run(t, h)
	n := alerts.Notification{Alert: store.Alert{ID: 5, ServerName: "db", Severity: "crit", Message: "pg down"}, Diagnosis: "OOMKilled"}
	if !h.Notify(n) {
		t.Fatal("diagnosis not accepted")
	}
	wait(t, fs, 1)
	got := fs.list()[0]
	if !strings.Contains(got.title, "AI diagnosis · db") || !strings.Contains(got.body, "#5 pg down\n\nOOMKilled") {
		t.Fatalf("diagnosis = %+v", got)
	}
	if len(tg.notified) != 1 || tg.notified[0].Diagnosis == "" {
		t.Fatal("telegram did not get the diagnosis")
	}
}
