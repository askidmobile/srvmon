package telegram

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/store"
)

func TestDiagnosisRepliesToAlertMessage(t *testing.T) {
	e := newBotEnv(t)
	ctx := context.Background()
	tok := testToken
	if err := e.bot.Configure(ctx, &tok, 42); err != nil {
		t.Fatal(err)
	}
	e.run(t)
	ruleID, err := e.st.CreateRule(ctx, store.Rule{Kind: store.KindContainerDown, Enabled: true, Severity: store.SevCrit,
		Channel: store.ChanTelegram})
	if err != nil {
		t.Fatal(err)
	}
	a, err := e.st.CreateAlert(ctx, store.Alert{RuleID: ruleID, Kind: store.KindContainerDown, ServerID: e.srvID,
		ServerName: "shop-db", Subject: "pg", Severity: store.SevCrit, Channel: store.ChanTelegram,
		State: store.AlertFiring, Message: "container pg crashed (exit 137)", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	// Not notified yet: the diagnosis has nothing to attach to.
	if e.engine.NotifyDiagnosis(ctx, a.ID, "x") {
		t.Fatal("diagnosis sent before the alert")
	}
	_ = e.st.MarkNotified(ctx, a.ID)
	a.Notified = true
	if !e.bot.Notify(alerts.Notification{Alert: a}) {
		t.Fatal("alert not queued")
	}
	e.api.waitSent(t, 1)
	// The sender records the message id right after the send.
	deadline := time.Now().Add(2 * time.Second)
	for {
		e.bot.mu.Lock()
		_, ok := e.bot.sent[a.ID]
		e.bot.mu.Unlock()
		if ok || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !e.engine.NotifyDiagnosis(ctx, a.ID, "OOM: `docker inspect pg` shows OOMKilled <true>") {
		t.Fatal("diagnosis not queued")
	}
	sent := e.api.waitSent(t, 2)
	d := sent[1]
	text := d["text"].(string)
	if !strings.Contains(text, "<code>docker inspect pg</code>") || !strings.Contains(text, "&lt;true&gt;") ||
		!strings.Contains(text, "#"+strconv.FormatInt(a.ID, 10)) {
		t.Fatalf("text = %q", text)
	}
	rp, _ := d["reply_parameters"].(map[string]any)
	if rp == nil || rp["message_id"] != float64(1001) {
		t.Fatalf("reply = %v", d["reply_parameters"])
	}
	if d["reply_markup"] != nil {
		t.Fatal("a diagnosis has no buttons")
	}
	// A warning's diagnosis waits for no one in quiet hours: not sent.
	w, _ := e.st.CreateAlert(ctx, store.Alert{RuleID: ruleID, Kind: store.KindContainerDown, ServerID: e.srvID,
		ServerName: "shop-db", Subject: "redis", Severity: store.SevWarn, Channel: store.ChanTelegram,
		State: store.AlertFiring, Message: "warn", StartedAt: time.Now(), Notified: true})
	now := time.Now().UTC()
	_ = alerts.SaveQuiet(ctx, e.st, alerts.Quiet{From: now.Add(-time.Hour).Format("15:04"), To: now.Add(time.Hour).Format("15:04"), TZ: "UTC"})
	if e.engine.NotifyDiagnosis(ctx, w.ID, "x") {
		t.Fatal("warning diagnosis sent in quiet hours")
	}
	_ = alerts.SaveQuiet(ctx, e.st, alerts.Quiet{})
	// Silenced meanwhile: nothing more is sent.
	if _, _, err := e.engine.Silence(ctx, a.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if e.engine.NotifyDiagnosis(ctx, a.ID, "again") {
		t.Fatal("sent while silenced")
	}
}

func TestCodeSpans(t *testing.T) {
	for in, want := range map[string]string{
		"run `df -h` now": "run <code>df -h</code> now",
		"a `b` c `d`":     "a <code>b</code> c <code>d</code>",
		"odd ` tick":      "odd ` tick",
		"none":            "none",
	} {
		if got := codeSpans(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
}
