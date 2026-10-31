package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

func (e *env) login(t *testing.T) {
	t.Helper()
	e.post(t, "/api/auth/password", map[string]string{"password": "hunter2"})
	code, _ := auth.TOTPCode(e.secret, auth.TOTPStep(time.Now()))
	if r := e.post(t, "/api/auth/totp", map[string]string{"code": code}); r.StatusCode != http.StatusOK {
		t.Fatalf("totp: %d", r.StatusCode)
	}
}

func (e *env) ingest(t *testing.T, token string, reports ...protocol.Report) protocol.IngestResponse {
	t.Helper()
	body, _ := json.Marshal(reports)
	req, _ := http.NewRequest(http.MethodPost, e.ts.URL+"/api/ingest", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ingest: %d", resp.StatusCode)
	}
	var out protocol.IngestResponse
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out
}

func TestServerScreenRequestsDetail(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "shop-db", Group: "prod"})
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("agent-token"))
	now := time.Now().UTC()

	if r := e.ingest(t, "agent-token", protocol.Report{Timestamp: now}); r.Detail {
		t.Fatal("detail requested while nobody watches")
	}
	e.login(t)
	resp := e.get(t, "/api/servers/"+itoa(id))
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("server: %d", resp.StatusCode)
	}
	rep := protocol.Report{
		Timestamp: now.Add(2 * time.Second),
		Host:      protocol.HostMetrics{CPUPercent: 30},
		Detail: &protocol.Detail{
			Cores: []float64{10, 50},
			Procs: []protocol.Process{{PID: 1, Name: "init"}},
			Logs:  []protocol.LogLine{{Time: now, Level: "ERR", Source: "kernel", Msg: "oom"}},
		},
		Inventory: &protocol.Inventory{OS: "Ubuntu 24.04"},
	}
	if r := e.ingest(t, "agent-token", rep); !r.Detail {
		t.Fatal("detail not requested while the screen is open")
	}

	resp = e.get(t, "/api/servers/"+itoa(id))
	defer resp.Body.Close()
	var v struct {
		Name      string              `json:"name"`
		Online    bool                `json:"online"`
		Detail    *protocol.Detail    `json:"detail"`
		Inventory *protocol.Inventory `json:"inventory"`
		Logs      []protocol.LogLine  `json:"logs"`
		Snapshot  struct {
			Report map[string]any `json:"report"`
		} `json:"snapshot"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		t.Fatal(err)
	}
	if v.Name != "shop-db" || !v.Online || v.Detail == nil || len(v.Detail.Cores) != 2 ||
		v.Inventory == nil || v.Inventory.OS != "Ubuntu 24.04" || len(v.Logs) != 1 {
		t.Fatalf("view = %+v", v)
	}
	if _, ok := v.Snapshot.Report["detail"]; ok {
		t.Fatal("snapshot must not carry detail (keeps /api/servers small)")
	}

	if r := e.get(t, "/api/servers/999"); r.StatusCode != http.StatusNotFound {
		t.Fatalf("missing server: %d", r.StatusCode)
	}
}

func TestHistoryFromMemory(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "web"})
	_ = e.st.AddAgentToken(ctx, id, auth.HashToken("tok"))
	now := time.Now().UTC()
	var reps []protocol.Report
	for i := 0; i < 5; i++ {
		reps = append(reps, protocol.Report{
			Timestamp: now.Add(time.Duration(i-5) * 5 * time.Second),
			Host: protocol.HostMetrics{CPUPercent: 50, Net: []protocol.NetIface{
				{Name: "eth0", RxBytes: uint64(i) * 5000, TxBytes: uint64(i) * 500},
			}},
		})
	}
	e.ingest(t, "tok", reps...)
	e.login(t)
	resp := e.get(t, "/api/servers/"+itoa(id)+"/history?range=15m")
	defer resp.Body.Close()
	var h struct {
		CPU []*float64 `json:"cpu"`
		Rx  []*float64 `json:"rx"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		t.Fatal(err)
	}
	if len(h.CPU) != 96 || h.CPU[95] == nil || *h.CPU[95] != 50 {
		t.Fatalf("cpu tail = %v", h.CPU[90:])
	}
	if h.Rx[95] == nil || *h.Rx[95] != 1000 {
		t.Fatalf("rx tail = %v", h.Rx[95])
	}
	if r := e.get(t, "/api/servers/"+itoa(id)+"/history?range=2y"); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad range: %d", r.StatusCode)
	}
}

func TestUpdateServerCard(t *testing.T) {
	e := newEnv(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "web"})
	e.login(t)
	patch := func(body string) *http.Response {
		req, _ := http.NewRequest(http.MethodPatch, e.ts.URL+"/api/servers/"+itoa(id), bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	r := patch(`{"description":" api ","group":"prod","tags":["#api","api","db"],"cost":4900,"currency":"RUB","next_payment":"2026-11-14"}`)
	defer r.Body.Close()
	var srv store.Server
	_ = json.NewDecoder(r.Body).Decode(&srv)
	if r.StatusCode != http.StatusOK || srv.Description != "api" || srv.Group != "prod" ||
		len(srv.Tags) != 2 || srv.Cost == nil || *srv.Cost != 4900 || srv.NextPayment != "2026-11-14" {
		t.Fatalf("update: %d %+v", r.StatusCode, srv)
	}
	if r := patch(`{"next_payment":"14.11.2026"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad date accepted: %d", r.StatusCode)
	}
	if r := patch(`{"name":"other"}`); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("name must not be editable: %d", r.StatusCode)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestUpdateServerCardKeepsOmittedBilling(t *testing.T) {
	e := newEnv(t)
	id, _ := e.st.CreateServer(context.Background(), store.Server{Name: "web", Period: "year", PanelURL: "https://p.example"})
	e.login(t)
	patch := func(body string) int {
		req, _ := http.NewRequest(http.MethodPatch, e.ts.URL+"/api/servers/"+itoa(id), bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := e.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := patch(`{"description":"x"}`); c != http.StatusOK {
		t.Fatalf("patch: %d", c)
	}
	srv, _ := e.st.GetServer(context.Background(), id)
	if srv.Period != "year" || srv.PanelURL != "https://p.example" {
		t.Fatalf("omitted fields were wiped: %+v", srv)
	}
	if c := patch(`{"panel_url":"javascript:alert(1)"}`); c != http.StatusBadRequest {
		t.Fatalf("bad panel url accepted: %d", c)
	}
	if c := patch(`{"period":"week"}`); c != http.StatusBadRequest {
		t.Fatalf("bad period accepted: %d", c)
	}
}
