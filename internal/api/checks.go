package api

import (
	"context"
	"net/http"
	"strings"

	"github.com/askidmobile/srvmon/internal/checks"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// checkSummary is the compact check status of a server for the overview.
type checkSummary struct {
	Total int `json:"total"`
	Down  int `json:"down"`
}

func (s *Server) decorateChecks(v *serverView) {
	if s.checks == nil {
		return
	}
	if total, down := s.checks.Summary(v.ID); total > 0 {
		v.Checks = &checkSummary{Total: total, Down: down}
	}
}

// handleChecks returns the checks of a server with their latest results and
// the expiry of the domains behind them.
func (s *Server) handleChecks(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.serverFromPath(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, s.checks.State(srv.ID))
}

// handleChecksUpdate replaces the targets of a server:
// {"targets": ["https://example.com", "db.example.com:5432"]}.
func (s *Server) handleChecksUpdate(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.serverFromPath(w, r)
	if !ok {
		return
	}
	var req struct {
		Targets []string `json:"targets"`
	}
	if err := readJSONLimit(r, &req, 32<<10); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	targets, msg := normalizeTargets(req.Targets)
	if msg != "" {
		s.writeValidation(w, msg)
		return
	}
	if err := s.store.SetServerChecks(r.Context(), srv.ID, targets); err != nil {
		s.internalError(w, "set checks", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "server_checks_updated", srv.Name+": "+strings.Join(targets, " "))
	if _, err := s.checks.Reload(context.WithoutCancel(r.Context())); err != nil {
		s.log.Error("checks: reload", "err", err)
	}
	s.checks.Kick()
	writeJSON(w, http.StatusOK, s.checks.State(srv.ID))
}

// normalizeTargets validates targets, drops duplicates and empty lines, and
// returns a message for the UI on the first invalid one.
func normalizeTargets(in []string) ([]string, string) {
	out := []string{}
	seen := map[string]bool{}
	for _, raw := range in {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		t, err := checks.Parse(raw)
		if err != nil {
			return nil, err.Error()
		}
		if seen[t.Raw] {
			continue
		}
		seen[t.Raw] = true
		out = append(out, t.Raw)
	}
	if len(out) > store.MaxChecksPerServer {
		return nil, i18n.T("up to %d checks per server", store.MaxChecksPerServer)
	}
	return out, ""
}
