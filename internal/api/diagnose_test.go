package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/store"
)

func (a *agentEnv) alert(t *testing.T, sev, subject string) store.Alert {
	t.Helper()
	ctx := context.Background()
	rule, err := a.st.CreateRule(ctx, store.Rule{Kind: store.KindContainerDown, Enabled: true, Severity: sev, Channel: store.ChanWeb})
	if err != nil {
		t.Fatal(err)
	}
	al, err := a.st.CreateAlert(ctx, store.Alert{RuleID: rule, Kind: store.KindContainerDown, ServerID: a.srvID,
		ServerName: "prod-api-01", Subject: subject, Severity: sev, Channel: store.ChanWeb, State: store.AlertFiring,
		Message: "container " + subject + " crashed (exit 137)", StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	return al
}

func TestDiagnosisExecIsReadOnly(t *testing.T) {
	a := newAgentEnv(t, "http://127.0.0.1:1")
	al := a.alert(t, store.SevCrit, "pg")
	exec := func(cmd string, diag int64) execResult {
		var res execResult
		if code := a.tool(t, "POST", "/tools/exec", map[string]any{"server": "prod-api-01", "command": cmd, "diagnose_id": diag}, &res); code != 200 {
			t.Fatal(code)
		}
		return res
	}
	// No diagnosis of that alert is running.
	if r := exec("docker ps", al.ID); r.Status != "refused" {
		t.Fatalf("%+v", r)
	}
	a.srv.diag.budget.Store(al.ID, &diagBudget{left: 2})
	if r := exec("docker restart pg", al.ID); r.Status != "refused" || !strings.Contains(r.Note, "read-only") {
		t.Fatalf("%+v", r)
	}
	if acts, _ := a.st.ListAgentActions(context.Background(), "", 10); len(acts) != 0 {
		t.Fatalf("a diagnosis asked for a confirmation: %+v", acts)
	}
	for i := 0; i < 2; i++ {
		if r := exec("docker logs --tail 50 pg", al.ID); r.Status != "ok" {
			t.Fatalf("%d: %+v", i, r)
		}
	}
	if r := exec("docker ps", al.ID); r.Status != "refused" || !strings.Contains(r.Note, "budget") {
		t.Fatalf("%+v", r)
	}
	if n := len(a.sshd.commands()); n != 2 {
		t.Fatalf("ran %d commands", n)
	}
}

// diagAI answers diagnosis runs and records them.
func diagAI(t *testing.T) (*httptest.Server, func() []aiagent.RunRequest) {
	var mu sync.Mutex
	var runs []aiagent.RunRequest
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req aiagent.RunRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		runs = append(runs, req)
		mu.Unlock()
		enc := json.NewEncoder(w)
		enc.Encode(aiagent.Event{T: "done", Text: "Причина: OOM, `docker inspect pg`"})
	}))
	t.Cleanup(ts.Close)
	return ts, func() []aiagent.RunRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]aiagent.RunRequest(nil), runs...)
	}
}

func TestDiagnosisQueueLimitsAndResult(t *testing.T) {
	ai, runs := diagAI(t)
	a := newAgentEnv(t, ai.URL)
	ctx := context.Background()
	s := a.srv

	crit := a.alert(t, store.SevCrit, "pg")
	if _, err := s.queueDiagnosis(ctx, crit, "auto"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.queueDiagnosis(ctx, crit, "manual"); !errors.Is(err, errDiagRunning) {
		t.Fatalf("second run of a queued one: %v", err)
	}
	s.diagnose(ctx, <-s.diag.queue)
	d, err := a.st.GetDiagnosis(ctx, crit.ID)
	if err != nil || d.State != store.DiagDone || !strings.Contains(d.Text, "OOM") || d.Trigger != "auto" {
		t.Fatalf("%+v %v", d, err)
	}
	r := runs()
	if len(r) != 1 || r[0].DiagnoseID != crit.ID || r[0].Server != "prod-api-01" || !strings.Contains(r[0].Text, "READ-ONLY") {
		t.Fatalf("%+v", r)
	}

	// Warnings are not diagnosed by default.
	warn := a.alert(t, store.SevWarn, "redis")
	if _, err := s.queueDiagnosis(ctx, warn, "auto"); !errors.Is(err, errDiagOff) {
		t.Fatalf("warn: %v", err)
	}
	// The same rule, server and subject again within the cooldown: no run.
	again, _ := a.st.CreateAlert(ctx, store.Alert{RuleID: crit.RuleID, Kind: crit.Kind, ServerID: crit.ServerID,
		ServerName: crit.ServerName, Subject: crit.Subject, Severity: store.SevCrit, Channel: store.ChanWeb,
		State: store.AlertFiring, Message: "again", StartedAt: time.Now()})
	if _, err := s.queueDiagnosis(ctx, again, "auto"); !errors.Is(err, errDiagOff) {
		t.Fatalf("cooldown: %v", err)
	}

	// Hourly limit: the next one is skipped and says why.
	raw, _ := json.Marshal(diagSettings{Enabled: true, PerHour: 1, PerDay: 5})
	_ = a.st.SetSetting(ctx, settingDiagnose, string(raw))
	other := a.alert(t, store.SevCrit, "nginx")
	if _, err := s.queueDiagnosis(ctx, other, "auto"); !errors.Is(err, errDiagLimit) {
		t.Fatalf("limit: %v", err)
	}
	if d, _ := a.st.GetDiagnosis(ctx, other.ID); d.State != store.DiagSkipped || !strings.Contains(d.Text, "hour") {
		t.Fatalf("%+v", d)
	}

	// A refused rerun keeps the shown diagnosis.
	if _, err := s.queueDiagnosis(ctx, crit, "manual"); !errors.Is(err, errDiagLimit) {
		t.Fatalf("rerun over limit: %v", err)
	}
	if d, _ := a.st.GetDiagnosis(ctx, crit.ID); d.State != store.DiagDone {
		t.Fatalf("rerun overwrote the result: %+v", d)
	}
	// Reruns of one alert count every time: with room for one more run,
	// the first rerun goes, the second is refused.
	raw, _ = json.Marshal(diagSettings{Enabled: true, PerHour: 2, PerDay: 5})
	_ = a.st.SetSetting(ctx, settingDiagnose, string(raw))
	if _, err := s.queueDiagnosis(ctx, crit, "manual"); err != nil {
		t.Fatal(err)
	}
	s.diagnose(ctx, <-s.diag.queue)
	if _, err := s.queueDiagnosis(ctx, crit, "manual"); !errors.Is(err, errDiagLimit) {
		t.Fatalf("second rerun: %v", err)
	}
	raw, _ = json.Marshal(diagSettings{Enabled: true, PerHour: 1, PerDay: 5})
	_ = a.st.SetSetting(ctx, settingDiagnose, string(raw))

	// The alert list carries the diagnosis.
	var view alertsView
	resp := a.get(t, "/api/alerts")
	_ = json.NewDecoder(resp.Body).Decode(&view)
	resp.Body.Close()
	found := false
	for _, al := range view.Open {
		if al.ID == crit.ID && al.Diagnosis != nil && al.Diagnosis.State == store.DiagDone {
			found = true
		}
	}
	if !found {
		t.Fatalf("no diagnosis in %+v", view.Open)
	}

	// Manual run over the limit: 429 with the reason.
	resp = a.do(t, "POST", fmt.Sprintf("/api/alerts/%d/diagnose", warn.ID), nil)
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("manual over limit: %d", resp.StatusCode)
	}
	// Settings are validated.
	if resp := a.do(t, "PUT", "/api/agent/diagnose", map[string]any{"per_hour": 50, "per_day": 10}); resp.StatusCode != 400 {
		t.Fatalf("bad limits saved: %d", resp.StatusCode)
	}
	if resp := a.do(t, "PUT", "/api/agent/diagnose", map[string]any{"enabled": false, "per_hour": 2, "per_day": 10}); resp.StatusCode != 200 {
		t.Fatalf("save: %d", resp.StatusCode)
	}
	if set := s.loadDiagSettings(ctx); set.Enabled || set.PerHour != 2 {
		t.Fatalf("%+v", set)
	}
}

func TestAlertChatContinuesDiagnosis(t *testing.T) {
	ai, runs := diagAI(t)
	a := newAgentEnv(t, ai.URL)
	ctx := context.Background()
	al := a.alert(t, store.SevCrit, "pg")

	// No diagnosis yet: nothing to continue.
	if resp := a.do(t, "POST", fmt.Sprintf("/api/alerts/%d/chat", al.ID), nil); resp.StatusCode != 400 {
		t.Fatalf("without a diagnosis: %d", resp.StatusCode)
	}
	if _, err := a.srv.queueDiagnosis(ctx, al, "manual"); err != nil {
		t.Fatal(err)
	}
	a.srv.diagnose(ctx, <-a.srv.diag.queue)

	open := func() chatView {
		t.Helper()
		resp := a.do(t, "POST", fmt.Sprintf("/api/alerts/%d/chat", al.ID), nil)
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("open: %d", resp.StatusCode)
		}
		var c chatView
		_ = json.NewDecoder(resp.Body).Decode(&c)
		return c
	}
	c := open()
	if c.ServerID == nil || *c.ServerID != a.srvID || !strings.HasPrefix(c.Title, diagChatPrefix(al.ID)) {
		t.Fatalf("%+v", c)
	}
	msgs, _ := a.st.AgentMessages(ctx, c.ID)
	if len(msgs) != 1 || msgs[0].Role != "assistant" || !strings.Contains(msgs[0].Text, "OOM") {
		t.Fatalf("%+v", msgs)
	}
	// A second click reopens the same chat.
	if again := open(); again.ID != c.ID {
		t.Fatalf("new chat %d instead of %d", again.ID, c.ID)
	}

	// The first question carries the diagnosis to the model, the next ones do not.
	before := len(runs())
	for i, q := range []string{"а что с памятью?", "и что делать?"} {
		resp := a.do(t, "POST", fmt.Sprintf("/api/agent/chats/%d/run", c.ID), map[string]string{"text": q})
		_, _ = io.Copy(io.Discard, resp.Body) // the run ends with the stream
		resp.Body.Close()
		r := runs()
		if len(r) != before+i+1 {
			t.Fatalf("runs: %d", len(r))
		}
		got := r[len(r)-1]
		if got.Server != "prod-api-01" || got.DiagnoseID != 0 || !strings.HasSuffix(got.Text, q) {
			t.Fatalf("%+v", got)
		}
		if seeded := strings.Contains(got.Text, "OOM"); seeded != (i == 0) {
			t.Fatalf("question %d seeded=%v: %q", i, seeded, got.Text)
		}
	}
	// The transcript shows the question as typed, without the context.
	msgs, _ = a.st.AgentMessages(ctx, c.ID)
	if len(msgs) < 2 || msgs[1].Role != "user" || msgs[1].Text != "а что с памятью?" {
		t.Fatalf("%+v", msgs)
	}
}

func TestSeededContextSurvivesFailedRun(t *testing.T) {
	diag := store.AgentMessage{Role: "assistant", Text: "OOM"}
	q := store.AgentMessage{Role: "user", Text: "?"}
	if seededContext([]store.AgentMessage{diag}) == "" {
		t.Fatal("no seed before the first question")
	}
	failed := store.AgentMessage{Role: "assistant", Error: "srvmon-ai: connection refused"}
	if !strings.Contains(seededContext([]store.AgentMessage{diag, q, failed}), "OOM") {
		t.Fatal("seed lost after a run that never reached the model")
	}
	ok := store.AgentMessage{Role: "assistant", Text: "answer"}
	if seededContext([]store.AgentMessage{diag, q, failed, q, ok}) != "" {
		t.Fatal("seed resent after the model answered")
	}
	if seededContext([]store.AgentMessage{q, ok}) != "" {
		t.Fatal("seed in an ordinary chat")
	}
}

func TestAlertChatReopensPastHistoryPage(t *testing.T) {
	ai, _ := diagAI(t)
	a := newAgentEnv(t, ai.URL)
	ctx := context.Background()
	al := a.alert(t, store.SevCrit, "pg")
	if _, err := a.srv.queueDiagnosis(ctx, al, "manual"); err != nil {
		t.Fatal(err)
	}
	a.srv.diagnose(ctx, <-a.srv.diag.queue)
	var first, again chatView
	if code := a.postJSON(t, fmt.Sprintf("/api/alerts/%d/chat", al.ID), nil, &first); code != 200 {
		t.Fatal(code)
	}
	for i := 0; i < 105; i++ {
		c, _ := a.st.CreateAgentChat(ctx, nil, "")
		_, _ = a.st.AddAgentMessage(ctx, c.ID, store.AgentMessage{Role: "user", Text: "hi"})
	}
	if code := a.postJSON(t, fmt.Sprintf("/api/alerts/%d/chat", al.ID), nil, &again); code != 200 || again.ID != first.ID {
		t.Fatalf("%d: chat %d, want %d", code, again.ID, first.ID)
	}
}
