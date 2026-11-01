package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/askidmobile/srvmon/internal/checks"
	"github.com/askidmobile/srvmon/internal/store"
)

func TestChecksAPI(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	id, _ := e.st.CreateServer(ctx, store.Server{Name: "shop"})
	path := fmt.Sprintf("/api/servers/%d/checks", id)
	if r := e.get(t, path); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without session: %d", r.StatusCode)
	}
	e.login(t)
	put := func(targets ...string) (*http.Response, checks.ServerState) {
		r := e.do(t, http.MethodPut, path, map[string]any{"targets": targets})
		var st checks.ServerState
		_ = json.NewDecoder(r.Body).Decode(&st)
		return r, st
	}
	r, st := put("https://Shop.Example.com", "", "db.example.com:5432", "https://shop.example.com/")
	if r.StatusCode != http.StatusOK || len(st.Checks) != 2 || st.Checks[0].Target != "https://shop.example.com/" ||
		st.Checks[0].Domain != "example.com" || !st.Checks[0].TLS || st.Checks[1].Kind != checks.KindTCP {
		t.Fatalf("put: %d %+v", r.StatusCode, st)
	}
	saved, _ := e.st.ServerChecks(ctx, id)
	if len(saved) != 2 {
		t.Fatalf("stored: %+v", saved)
	}
	if r, _ := put("ftp://x.org"); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad target: %d", r.StatusCode)
	}
	many := make([]string, store.MaxChecksPerServer+1)
	for i := range many {
		many[i] = fmt.Sprintf("h%d.example.com:80", i)
	}
	if r, _ := put(many...); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("too many: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPut, "/api/servers/999/checks", map[string]any{"targets": []string{}}); r.StatusCode != http.StatusNotFound {
		t.Fatalf("unknown server: %d", r.StatusCode)
	}
	// The overview carries a summary.
	list := []serverView{}
	r = e.get(t, "/api/servers")
	_ = json.NewDecoder(r.Body).Decode(&list)
	if len(list) != 1 || list[0].Checks == nil || list[0].Checks.Total != 2 {
		t.Fatalf("summary: %+v", list)
	}
	// Clearing removes them.
	if r, st := put(); r.StatusCode != http.StatusOK || len(st.Checks) != 0 {
		t.Fatalf("clear: %+v", st)
	}
}
