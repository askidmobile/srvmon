package api

import (
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/store"
)

// serverDetailView is the payload of the server screen.
type serverDetailView struct {
	serverView
	metrics.Details
}

func (s *Server) serverFromPath(w http.ResponseWriter, r *http.Request) (store.Server, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not_found")
		return store.Server{}, false
	}
	srv, err := s.store.GetServer(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return srv, false
	} else if err != nil {
		s.log.Error("get server", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return srv, false
	}
	return srv, true
}

// handleServer returns everything the server screen shows. Polling it marks
// the screen as open, so the agent switches to detailed reports.
func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.serverFromPath(w, r)
	if !ok {
		return
	}
	s.latest.Watch(srv.ID)
	v := serverDetailView{serverView: serverView{Server: srv}, Details: s.latest.Details(srv.ID, 200)}
	if snap, ok := s.latest.Get(srv.ID); ok {
		v.Snapshot = &snap
		v.Online = s.now().Sub(snap.Received) < offlineAfter
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.serverFromPath(w, r)
	if !ok {
		return
	}
	rng := r.URL.Query().Get("range")
	if rng == "" {
		rng = "15m"
	}
	span, ok := metrics.Ranges[rng]
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_range")
		return
	}
	end := s.now()
	series, err := s.vmRead.ServerSeries(r.Context(), srv.Name, rng, end)
	if err != nil {
		// Still answer from memory; the graph just covers less time.
		s.log.Warn("victoriametrics query", "server", srv.Name, "err", err)
		series = nil
	}
	mem := s.latest.Points(srv.ID, end.Add(-span))
	writeJSON(w, http.StatusOK, metrics.BuildHistory(rng, end, series, mem))
}

var datePattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

func (s *Server) handleServerCard(w http.ResponseWriter, r *http.Request) {
	srv, ok := s.serverFromPath(w, r)
	if !ok {
		return
	}
	// Fields the client leaves out keep their value (older clients do not
	// send the billing period or the panel link).
	c := store.ServerCard{Period: srv.Period, PanelURL: srv.PanelURL}
	if err := readJSON(r, &c); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if field := validateCard(&c); field != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "field": field})
		return
	}
	if err := s.store.UpdateServerCard(r.Context(), srv.ID, c); err != nil {
		s.log.Error("update server", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "server_card_updated", srv.Name)
	updated, err := s.store.GetServer(r.Context(), srv.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

// validateCard trims fields and returns the name of the first invalid one.
func validateCard(c *store.ServerCard) string {
	fields := []struct {
		name string
		v    *string
		max  int
	}{
		{"description", &c.Description, 500},
		{"project", &c.Project, 64},
		{"group", &c.Group, 64},
		{"provider", &c.Provider, 64},
		{"plan", &c.Plan, 128},
		{"currency", &c.Currency, 8},
		{"next_payment", &c.NextPayment, 10},
	}
	for _, f := range fields {
		*f.v = strings.TrimSpace(*f.v)
		if utf8.RuneCountInString(*f.v) > f.max {
			return f.name
		}
	}
	if c.NextPayment != "" && !datePattern.MatchString(c.NextPayment) {
		return "next_payment"
	}
	c.Period, c.PanelURL = strings.TrimSpace(c.Period), strings.TrimSpace(c.PanelURL)
	if !validPeriod(c.Period) {
		return "period"
	}
	if !validPanelURL(c.PanelURL) {
		return "panel_url"
	}
	if c.Cost != nil && (*c.Cost < 0 || *c.Cost > 1e9) {
		return "cost"
	}
	if len(c.Tags) > 20 {
		return "tags"
	}
	tags := make([]string, 0, len(c.Tags))
	seen := map[string]bool{}
	for _, t := range c.Tags {
		t = strings.TrimPrefix(strings.TrimSpace(t), "#")
		if t == "" || seen[t] {
			continue
		}
		if utf8.RuneCountInString(t) > 32 || strings.ContainsAny(t, " \t\n,") {
			return "tags"
		}
		seen[t] = true
		tags = append(tags, t)
	}
	c.Tags = tags
	return ""
}
