package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/askidmobile/srvmon/internal/notify"
	"github.com/askidmobile/srvmon/internal/secret"
)

type recSender struct {
	mu   sync.Mutex
	urls []string
}

func (r *recSender) Check(raw string) error {
	if !strings.Contains(raw, "://") {
		return errors.New("bad url")
	}
	return nil
}

func (r *recSender) Send(_ context.Context, raw, _, _ string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.urls = append(r.urls, raw)
	return nil
}

func TestNotifyChannelsAPI(t *testing.T) {
	rs := &recSender{}
	box, _ := secret.New(make([]byte, 32))
	e := newEnv(t, func(o *Options) { o.Secrets, o.NotifySender = box, rs })
	if r := e.do(t, http.MethodPost, "/api/notify/channels", map[string]any{}); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("without session: %d", r.StatusCode)
	}
	e.login(t)
	secretURL := "ntfy://ntfy.sh/top-secret-topic"
	r := e.do(t, http.MethodPost, "/api/notify/channels",
		map[string]any{"name": "phone", "url": secretURL, "min_severity": "warn", "enabled": true})
	if r.StatusCode != http.StatusCreated {
		t.Fatalf("create: %d", r.StatusCode)
	}
	var v notify.View
	_ = json.NewDecoder(r.Body).Decode(&v)
	if v.ID == 0 || v.Service != "ntfy" || v.Target != "ntfy.sh" {
		t.Fatalf("view = %+v", v)
	}
	if r := e.do(t, http.MethodPost, "/api/notify/channels",
		map[string]any{"name": "x", "url": "nope", "min_severity": "warn"}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad url: %d", r.StatusCode)
	}

	// The list never carries the URL.
	r = e.get(t, "/api/notify")
	var nv notifyView
	var raw json.RawMessage
	_ = json.NewDecoder(r.Body).Decode(&raw)
	_ = json.Unmarshal(raw, &nv)
	if strings.Contains(string(raw), "secret") || len(nv.Channels) != 1 || !nv.ChannelsOK || len(nv.Services) == 0 {
		t.Fatalf("notify view = %s", raw)
	}

	if r := e.do(t, http.MethodPut, "/api/notify/channels/1",
		map[string]any{"name": "phone", "min_severity": "crit", "enabled": true}); r.StatusCode != http.StatusOK {
		t.Fatalf("update: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPost, "/api/notify/channels/1/test", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("test: %d", r.StatusCode)
	}
	if len(rs.urls) != 1 || rs.urls[0] != secretURL {
		t.Fatalf("sent to %v", rs.urls)
	}
	var sum alertSummary
	r = e.get(t, "/api/alerts/summary")
	_ = json.NewDecoder(r.Body).Decode(&sum)
	if sum.Channels != 1 || sum.Failing != 0 {
		t.Fatalf("summary = %+v", sum)
	}
	if r := e.do(t, http.MethodDelete, "/api/notify/channels/1", nil); r.StatusCode != http.StatusOK {
		t.Fatalf("delete: %d", r.StatusCode)
	}
	if r := e.do(t, http.MethodPost, "/api/notify/channels/1/test", nil); r.StatusCode != http.StatusNotFound {
		t.Fatalf("test deleted: %d", r.StatusCode)
	}
}

func TestNotifyChannelsNeedMasterKey(t *testing.T) {
	e := newEnv(t)
	e.login(t)
	r := e.do(t, http.MethodPost, "/api/notify/channels",
		map[string]any{"name": "phone", "url": "ntfy://ntfy.sh/x", "min_severity": "warn", "enabled": true})
	if r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("create without master key: %d", r.StatusCode)
	}
}
