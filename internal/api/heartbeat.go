package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/heartbeat"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// selfChecks are what the heartbeat verifies before it pings: the panel is
// up, but is it doing its job?
func (s *Server) selfChecks() []heartbeat.Check {
	checks := []heartbeat.Check{
		{Name: "db", Run: func(ctx context.Context) error {
			_, err := s.store.GetSetting(ctx, "healthcheck")
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
			return nil
		}},
		{Name: "alerts", Run: func(context.Context) error { return s.alerts.Healthy() }},
		{Name: "telegram", Run: func(context.Context) error {
			if s.bot == nil {
				return nil
			}
			st := s.bot.Status()
			if st.Configured && !st.Connected && st.LastError != "" {
				return errors.New(st.LastError)
			}
			return nil
		}},
	}
	if vm := strings.TrimRight(s.cfg.VMURL, "/"); vm != "" {
		client := &http.Client{Timeout: 5 * time.Second}
		checks = append(checks, heartbeat.Check{Name: "victoriametrics", Run: func(ctx context.Context) error {
			req, err := http.NewRequestWithContext(ctx, http.MethodGet, vm+"/health", nil)
			if err != nil {
				return err
			}
			resp, err := client.Do(req)
			if err != nil {
				return fmt.Errorf("%s", i18n.T("no connection: %s", err.Error()))
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				return fmt.Errorf("HTTP %d", resp.StatusCode)
			}
			return nil
		}})
	}
	return checks
}

func (s *Server) handleHeartbeatSave(w http.ResponseWriter, r *http.Request) {
	if s.heartbeat == nil {
		writeError(w, http.StatusServiceUnavailable, "heartbeat_unavailable")
		return
	}
	var req struct {
		URL          *string `json:"url"` // absent: keep, "": remove
		FailEndpoint bool    `json:"fail_endpoint"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	err := s.heartbeat.Configure(r.Context(), req.URL, req.FailEndpoint)
	switch {
	case errors.Is(err, heartbeat.ErrBadURL):
		s.writeValidation(w, i18n.T("the ping URL starts with http:// or https:// and has no spaces"))
		return
	case errors.Is(err, heartbeat.ErrNoSecrets):
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
		return
	case err != nil:
		s.internalError(w, "heartbeat: configure", err)
		return
	}
	detail := "fail_endpoint=" + fmt.Sprint(req.FailEndpoint)
	if req.URL != nil {
		detail += " url=" + map[bool]string{true: "removed", false: "set"}[*req.URL == ""]
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "heartbeat_config", detail)
	writeJSON(w, http.StatusOK, s.heartbeat.Status())
}

func (s *Server) handleHeartbeatTest(w http.ResponseWriter, r *http.Request) {
	if s.heartbeat == nil {
		writeError(w, http.StatusServiceUnavailable, "heartbeat_unavailable")
		return
	}
	if err := s.heartbeat.Test(r.Context()); err != nil {
		if errors.Is(err, heartbeat.ErrNotConfigured) {
			s.writeValidation(w, i18n.T("set the ping URL first"))
			return
		}
		s.writeValidation(w, i18n.T("not sent: %s", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, s.heartbeat.Status())
}
