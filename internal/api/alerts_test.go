package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/askidmobile/srvmon/internal/store"
)

func (e *env) do(t *testing.T, method, path string, body any) *http.Response {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.ts.URL+path, &buf)
	req.Header.Set("Content-Type", "application/json")
	resp, err := e.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestAlertRulesAPI(t *testing.T) {
	e := newEnv(t)
	if r := e.get(t, "/api/alert-rules"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("rules without session: %d", r.StatusCode)
	}
	e.login(t)
	var rules []store.Rule
	r := e.get(t, "/api/alert-rules")
	_ = json.NewDecoder(r.Body).Decode(&rules)
	r.Body.Close()
	if len(rules) != 13 {
		t.Fatalf("default rules = %d", len(rules))
	}
	cpu := rules[1]
	cpu.Threshold = 150
	if r := e.do(t, http.MethodPut, "/api/alert-rules/2", cpu); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("cpu 150%% accepted: %d", r.StatusCode)
	}
	cpu.Threshold, cpu.Kind = 80, "login" // kind is fixed
	if r := e.do(t, http.MethodPut, "/api/alert-rules/2", cpu); r.StatusCode != http.StatusOK {
		t.Fatalf("update: %d", r.StatusCode)
	}
	got, _ := e.st.GetRule(context.Background(), 2)
	if got.Threshold != 80 || got.Kind != store.KindCPU {
		t.Fatalf("rule = %+v", got)
	}
	nr := store.Rule{Kind: store.KindCPU, Enabled: true, Threshold: 98, DurationSec: 60, Severity: "crit", Channel: "tg"}
	if r := e.do(t, http.MethodPost, "/api/alert-rules", nr); r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodDelete, "/api/alert-rules/11", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", r.StatusCode)
	}
}

func TestLoginAndBruteForceEvents(t *testing.T) {
	e := newEnv(t)
	for i := 0; i < 5; i++ {
		e.post(t, "/api/auth/password", map[string]string{"password": "nope"})
	}
	e.srv.limiter.Reset("127.0.0.1")
	e.login(t)
	var v alertsView
	r := e.get(t, "/api/alerts")
	_ = json.NewDecoder(r.Body).Decode(&v)
	r.Body.Close()
	kinds := map[string]int{}
	for _, a := range v.Recent {
		kinds[a.Kind]++
	}
	if kinds[store.KindBruteforce] != 1 || kinds[store.KindLogin] != 1 {
		t.Fatalf("events = %v", kinds)
	}
	if v.Last24h != 2 || len(v.Open) != 0 {
		t.Fatalf("view = %+v", v)
	}
	var sum alertSummary
	r = e.get(t, "/api/alerts/summary")
	_ = json.NewDecoder(r.Body).Decode(&sum)
	r.Body.Close()
	if sum.Telegram != "off" {
		t.Fatalf("telegram = %q", sum.Telegram)
	}
}

func TestQuietAndTelegramValidation(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	if r := e.do(t, http.MethodPut, "/api/notify/quiet", map[string]string{"from": "25:00", "to": "07:00"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad quiet: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPut, "/api/notify/quiet", map[string]string{"from": "23:00", "to": "07:00", "tz": "Europe/Moscow"}); r.StatusCode != http.StatusOK {
		t.Fatalf("quiet: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPut, "/api/notify/telegram", map[string]string{"chat_id": "abc"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad chat: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPost, "/api/notify/telegram/test", nil); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("test without config: %d", r.StatusCode)
	}
}
