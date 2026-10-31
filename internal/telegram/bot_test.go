package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

const testToken = "123456:ABC-test"

// fakeAPI is a minimal Bot API: it records sent messages and serves queued
// updates to getUpdates.
type fakeAPI struct {
	mu      sync.Mutex
	sent    []map[string]any
	calls   []string
	updates []map[string]any
	nextID  int64
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	token, method, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/bot"), "/")
	var params map[string]any
	_ = json.NewDecoder(r.Body).Decode(&params)
	if token != testToken {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, method)
	var result any = true
	switch method {
	case "getMe":
		result = map[string]any{"id": 1, "username": "srvmon_test_bot"}
	case "sendMessage":
		f.sent = append(f.sent, params)
	case "getUpdates":
		ups := f.updates
		f.updates = nil
		f.mu.Unlock()
		if len(ups) == 0 {
			select {
			case <-r.Context().Done():
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": ups})
		return
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "result": result})
}

func (f *fakeAPI) queue(u map[string]any) {
	f.mu.Lock()
	f.nextID++
	u["update_id"] = f.nextID
	f.updates = append(f.updates, u)
	f.mu.Unlock()
}

func (f *fakeAPI) messages() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.sent...)
}

func (f *fakeAPI) waitSent(t *testing.T, n int) []map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if m := f.messages(); len(m) >= n {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("want %d messages, got %+v", n, f.messages())
	return nil
}

type botEnv struct {
	bot    *Bot
	api    *fakeAPI
	st     *store.Store
	engine *alerts.Engine
	latest *metrics.Latest
	srvID  int64
}

func newBotEnv(t *testing.T) *botEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "tg.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	_ = alerts.SaveQuiet(ctx, st, alerts.Quiet{})
	box, err := secret.New(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeAPI{}
	ts := httptest.NewServer(fake)
	t.Cleanup(ts.Close)
	latest := metrics.NewLatest()
	engine := alerts.New(st, latest, nil)
	bot, err := New(ctx, Options{Store: st, Secrets: box, Engine: engine, Latest: latest, API: ts.URL})
	if err != nil {
		t.Fatal(err)
	}
	engine.SetNotifier(bot)
	id, _ := st.CreateServer(ctx, store.Server{Name: "shop-db"})
	return &botEnv{bot: bot, api: fake, st: st, engine: engine, latest: latest, srvID: id}
}

func (e *botEnv) run(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.bot.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
}

func msg(chat int64, text string) map[string]any {
	return map[string]any{"message": map[string]any{
		"message_id": 1, "date": time.Now().Unix(), "text": text,
		"chat": map[string]any{"id": chat, "type": "private", "username": "anton"},
	}}
}

func TestConfigureRejectsBadToken(t *testing.T) {
	e := newBotEnv(t)
	bad := "999:nope"
	if err := e.bot.Configure(context.Background(), &bad, 42); err != ErrBadToken {
		t.Fatalf("err = %v", err)
	}
	if e.bot.Status().Configured {
		t.Fatal("bad token stored")
	}
}

func TestTokenIsSealedAndReloaded(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 42); err != nil {
		t.Fatal(err)
	}
	raw, _ := e.st.GetSetting(ctx, keyToken)
	if strings.Contains(raw, "ABC-test") {
		t.Fatal("token stored in plain text")
	}
	// Keep the token when only the chat changes.
	if err := e.bot.Configure(ctx, nil, 43); err != nil {
		t.Fatal(err)
	}
	if tok, chat := e.bot.creds(); tok != testToken || chat != 43 {
		t.Fatalf("creds = %q %d", tok, chat)
	}
}

func TestUnknownChatThenCommands(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 0); err != nil {
		t.Fatal(err)
	}
	e.run(t)

	// Before a chat is set, /start tells the chat its id.
	e.api.queue(msg(777, "/start"))
	sent := e.api.waitSent(t, 1)
	if !strings.Contains(sent[0]["text"].(string), "777") {
		t.Fatalf("reply = %v", sent[0]["text"])
	}
	if p := e.bot.Status().PendingChat; p == nil || p.ID != 777 {
		t.Fatalf("pending = %+v", p)
	}

	if err := e.bot.Configure(ctx, nil, 777); err != nil {
		t.Fatal(err)
	}
	if !e.bot.Status().Connected {
		// Reconnect after the reload happens in the background.
		time.Sleep(200 * time.Millisecond)
	}
	// A stranger is ignored once the chat is set.
	e.api.queue(msg(555, "/status"))
	e.latest.Put(e.srvID, protocol.Report{Timestamp: time.Now(), Host: protocol.HostMetrics{CPUPercent: 42, MemTotal: 100, MemAvailable: 50}})
	e.api.queue(msg(777, "/status"))
	sent = e.api.waitSent(t, 2)
	text := sent[1]["text"].(string)
	if sent[1]["chat_id"].(float64) != 777 || !strings.Contains(text, "1/1 онлайн") || !strings.Contains(text, "cpu 42%") {
		t.Fatalf("status reply = %v", sent[1])
	}
	time.Sleep(100 * time.Millisecond)
	if n := len(e.api.messages()); n != 2 {
		t.Fatalf("stranger got an answer: %d messages", n)
	}
}

func TestNotifyAndAckButton(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 777); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	e.engine.Event(ctx, store.KindLogin, 0, "", "1.2.3.4", "вход в панель с 1.2.3.4")
	sent := e.api.waitSent(t, 1)
	if !strings.Contains(sent[0]["text"].(string), "вход в панель") {
		t.Fatalf("event = %v", sent[0])
	}

	// A firing alert carries ack / silence buttons.
	e.latest.Put(e.srvID, protocol.Report{Timestamp: time.Now(), Host: protocol.HostMetrics{
		Disks: []protocol.Disk{{Mount: "/", Total: 100, Used: 99}}}})
	if err := e.engine.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	sent = e.api.waitSent(t, 2)
	kb, _ := json.Marshal(sent[1]["reply_markup"])
	if !strings.Contains(string(kb), "ack:") {
		t.Fatalf("no buttons: %v", sent[1])
	}
	open, _ := e.st.OpenAlerts(ctx)
	if len(open) != 1 || !open[0].Notified {
		t.Fatalf("open = %+v", open)
	}
	e.api.queue(map[string]any{"callback_query": map[string]any{
		"id": "cb1", "data": "ack:" + jsonNum(open[0].ID),
		"message": map[string]any{"message_id": 5, "date": time.Now().Unix(), "chat": map[string]any{"id": 777}},
	}})
	e.api.waitSent(t, 3)
	a, _ := e.st.GetAlert(ctx, open[0].ID)
	if a.State != store.AlertAcked {
		t.Fatalf("state = %s", a.State)
	}
}

func jsonNum(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestBillsText(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	cost := 120.0
	due := time.Now().AddDate(0, 0, 1).Format("2006-01-02")
	_ = e.st.UpdateServerCard(ctx, e.srvID, store.ServerCard{Cost: &cost, Currency: "EUR", Period: "year", NextPayment: due})
	text := e.bot.billsText(ctx)
	if !strings.Contains(text, "🟡") || !strings.Contains(text, "В месяц: 10.00 EUR") {
		t.Fatalf("bills = %s", text)
	}
}

func TestLongMessageIsSentAsPlainText(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 777); err != nil {
		t.Fatal(err)
	}
	// Cyrillic plus an entity right at the cut: neither may be split.
	text := "<b>счета</b>\n" + strings.Repeat("я", 1993) + "&amp;" + strings.Repeat("x", 100)
	if err := e.bot.send(ctx, tok, 777, text, nil); err != nil {
		t.Fatal(err)
	}
	m := e.api.messages()[0]
	got := m["text"].(string)
	if _, html := m["parse_mode"]; html || len(got) > 4000 || !utf8.ValidString(got) ||
		strings.Contains(got, "<b>") || strings.Contains(got, "&amp") || !strings.HasSuffix(got, "…") {
		t.Fatalf("parse_mode=%v len=%d text tail=%q", m["parse_mode"], len(got), got[len(got)-20:])
	}
}

func TestAgentActionButtonsAndAsk(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 777); err != nil {
		t.Fatal(err)
	}
	decided := make(chan string, 1)
	e.bot.SetActionDecider(func(_ context.Context, id int64, approve bool) string {
		decided <- jsonNum(id) + map[bool]string{true: ":ok", false: ":no"}[approve]
		return "готово"
	})
	release := make(chan struct{})
	e.bot.SetAsk(func(context.Context, string) (string, error) {
		<-release // a slow agent must not block the polling loop
		return "диск в норме <ok>", nil
	})
	e.run(t)

	if !e.bot.SendAction(42, "🤖 restart nginx") {
		t.Fatal("SendAction refused")
	}
	sent := e.api.waitSent(t, 1)
	kb, _ := json.Marshal(sent[0]["reply_markup"])
	if !strings.Contains(string(kb), "act:42:ok") || !strings.Contains(string(kb), "act:42:no") {
		t.Fatalf("buttons = %s", kb)
	}

	e.api.queue(msg(777, "/ask что с диском?"))
	sent = e.api.waitSent(t, 2)
	if !strings.Contains(sent[1]["text"].(string), "думает") {
		t.Fatalf("ask ack = %v", sent[1])
	}
	// While the answer is pending, a button press is still handled.
	e.api.queue(map[string]any{"callback_query": map[string]any{
		"id": "cb1", "data": "act:42:ok",
		"message": map[string]any{"message_id": 5, "date": time.Now().Unix(), "chat": map[string]any{"id": 777}},
	}})
	select {
	case got := <-decided:
		if got != "42:ok" {
			t.Fatalf("decided %s", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("button not handled while /ask runs")
	}
	close(release)
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for _, m := range e.api.messages() {
			if strings.Contains(m["text"].(string), "диск в норме &lt;ok&gt;") {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no answer: %+v", e.api.messages())
}
