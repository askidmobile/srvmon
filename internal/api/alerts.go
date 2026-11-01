package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/heartbeat"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/notify"
	"github.com/askidmobile/srvmon/internal/store"
	"github.com/askidmobile/srvmon/internal/telegram"
)

func (s *Server) alertRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/alerts", s.handleAlerts)
	h("GET /api/alerts/summary", s.handleAlertSummary)
	h("POST /api/alerts/{id}/ack", s.handleAlertAck)
	h("POST /api/alerts/{id}/silence", s.handleAlertSilence)
	h("GET /api/alert-rules", s.handleRules)
	h("POST /api/alert-rules", s.handleRuleCreate)
	h("PUT /api/alert-rules/{id}", s.handleRuleUpdate)
	h("DELETE /api/alert-rules/{id}", s.handleRuleDelete)
	h("GET /api/notify", s.handleNotify)
	h("PUT /api/notify/telegram", s.handleTelegramSave)
	h("POST /api/notify/telegram/test", s.handleTelegramTest)
	h("PUT /api/notify/quiet", s.handleQuietSave)
	h("PUT /api/notify/heartbeat", s.handleHeartbeatSave)
	h("POST /api/notify/heartbeat/test", s.handleHeartbeatTest)
	h("POST /api/notify/channels", s.handleChannelCreate)
	h("PUT /api/notify/channels/{id}", s.handleChannelUpdate)
	h("DELETE /api/notify/channels/{id}", s.handleChannelDelete)
	h("POST /api/notify/channels/{id}/test", s.handleChannelTest)
}

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

func (s *Server) internalError(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	writeError(w, http.StatusInternalServerError, "internal")
}

type alertsView struct {
	Open    []store.Alert `json:"open"`
	Recent  []store.Alert `json:"recent"`
	Last24h int           `json:"last24h"`
}

func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {
	open, err := s.store.OpenAlerts(r.Context())
	if err != nil {
		s.internalError(w, "open alerts", err)
		return
	}
	recent, err := s.store.RecentAlerts(r.Context(), 200)
	if err != nil {
		s.internalError(w, "recent alerts", err)
		return
	}
	n, err := s.store.CountAlertsSince(r.Context(), s.now().Add(-24*time.Hour))
	if err != nil {
		s.internalError(w, "count alerts", err)
		return
	}
	if err := s.store.AttachDiagnoses(r.Context(), open, recent); err != nil {
		s.internalError(w, "alert diagnoses", err)
		return
	}
	writeJSON(w, http.StatusOK, alertsView{Open: open, Recent: recent, Last24h: n})
}

// alertSummary feeds the header of every page.
type alertSummary struct {
	Open     int           `json:"open"`
	Crit     int           `json:"crit"`
	Last24h  int           `json:"last24h"`
	Telegram string        `json:"telegram"` // off | ok | error
	Channels int           `json:"channels"` // enabled Shoutrrr channels
	Failing  int           `json:"channels_failing"`
	Latest   []store.Alert `json:"latest"` // newest open alerts, up to 5
}

func (s *Server) telegramState() string {
	if s.bot == nil {
		return "error"
	}
	st := s.bot.Status()
	switch {
	case !st.Configured || st.ChatID == "":
		return "off"
	case st.Connected:
		return "ok"
	case st.LastError != "":
		return "error"
	}
	return "off"
}

func (s *Server) handleAlertSummary(w http.ResponseWriter, r *http.Request) {
	open, err := s.store.OpenAlerts(r.Context())
	if err != nil {
		s.internalError(w, "open alerts", err)
		return
	}
	n, err := s.store.CountAlertsSince(r.Context(), s.now().Add(-24*time.Hour))
	if err != nil {
		s.internalError(w, "count alerts", err)
		return
	}
	out := alertSummary{Open: len(open), Last24h: n, Telegram: s.telegramState(), Latest: open[:min(5, len(open))]}
	if s.hub != nil {
		out.Channels, out.Failing = s.hub.Health()
	}
	for _, a := range open {
		if a.Severity == store.SevCrit {
			out.Crit++
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleAlertAck(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	a, err := s.alerts.Ack(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "ack alert", err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) handleAlertSilence(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	var req struct {
		Minutes int `json:"minutes"`
	}
	if err := readJSON(r, &req); err != nil || req.Minutes < 1 || req.Minutes > 7*24*60 {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	a, until, err := s.alerts.Silence(r.Context(), id, time.Duration(req.Minutes)*time.Minute)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "silence alert", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"alert": a, "until": until})
}

// --- rules ---

func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.store.ListRules(r.Context())
	if err != nil {
		s.internalError(w, "list rules", err)
		return
	}
	writeJSON(w, http.StatusOK, rules)
}

// validateRule checks a rule and returns a message for the UI, or "".
func validateRule(r *store.Rule) string {
	if !store.KnownKind(r.Kind) {
		return i18n.T("unknown rule type")
	}
	if r.Severity != store.SevCrit && r.Severity != store.SevWarn && r.Severity != store.SevInfo {
		return i18n.T("level: crit, warn or info")
	}
	if r.Channel != store.ChanTelegram && r.Channel != store.ChanWeb {
		return i18n.T("channel: tg or web")
	}
	if r.DurationSec < 0 || r.DurationSec > 7*24*3600 {
		return i18n.T("duration from 0 to 7 days")
	}
	switch r.Kind {
	case store.KindCPU, store.KindMem, store.KindDisk:
		if r.Threshold <= 0 || r.Threshold >= 100 {
			return i18n.T("threshold in percent: from 1 to 99")
		}
	case store.KindContainerRestarts:
		if r.Threshold < 0 || r.Threshold > 1000 || r.Threshold != float64(int(r.Threshold)) {
			return i18n.T("threshold: a whole number of restarts")
		}
		if r.DurationSec < 60 {
			return i18n.T("the restart counting window is at least 1 minute")
		}
	case store.KindDiskFill:
		if r.Threshold < 1 || r.Threshold > 24*30 {
			return i18n.T("threshold: from 1 hour to 30 days (720 hours)")
		}
		r.DurationSec = 0
	case store.KindPaymentDue:
		if r.Threshold < 0 || r.Threshold > 60 || r.Threshold != float64(int(r.Threshold)) {
			return i18n.T("threshold: from 0 to 60 days")
		}
		r.DurationSec = 0
	case store.KindCertExpiry, store.KindDomainExpiry:
		if r.Threshold < 0 || r.Threshold > 120 || r.Threshold != float64(int(r.Threshold)) {
			return i18n.T("threshold: from 0 to 120 days")
		}
		r.DurationSec = 0
	case store.KindAgentSilent:
		if r.DurationSec < 30 {
			return i18n.T("the agent sends data every 5 s: the threshold is at least 30 seconds")
		}
		r.Threshold = 0
	default: // container_down and events
		r.Threshold = 0
		if store.EventKind(r.Kind) {
			r.DurationSec = 0
		}
	}
	return ""
}

func (s *Server) writeValidation(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid", "message": msg})
}

func (s *Server) handleRuleCreate(w http.ResponseWriter, r *http.Request) {
	var rule store.Rule
	if err := readJSON(r, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if msg := validateRule(&rule); msg != "" {
		s.writeValidation(w, msg)
		return
	}
	id, err := s.store.CreateRule(r.Context(), rule)
	if err != nil {
		s.internalError(w, "create rule", err)
		return
	}
	rule.ID = id
	_ = s.store.Audit(r.Context(), s.clientIP(r), "alert_rule_create", fmt.Sprintf("%d %s", id, rule.Kind))
	writeJSON(w, http.StatusCreated, rule)
}

func (s *Server) handleRuleUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	cur, err := s.store.GetRule(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "get rule", err)
		return
	}
	var rule store.Rule
	if err := readJSON(r, &rule); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	rule.ID, rule.Kind = cur.ID, cur.Kind
	if msg := validateRule(&rule); msg != "" {
		s.writeValidation(w, msg)
		return
	}
	if err := s.store.UpdateRule(r.Context(), rule); err != nil {
		s.internalError(w, "update rule", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "alert_rule_update", strconv.FormatInt(id, 10))
	if rule.Threshold != cur.Threshold || rule.DurationSec != cur.DurationSec {
		s.alerts.ResetRule(id)
	}
	s.reevaluate(r)
	writeJSON(w, http.StatusOK, rule)
}

func (s *Server) handleRuleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	if err := s.store.DeleteRule(r.Context(), id); errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "delete rule", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "alert_rule_delete", strconv.FormatInt(id, 10))
	s.reevaluate(r)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// reevaluate applies a rule change right away (disabled rules close their
// alerts) instead of on the next tick.
func (s *Server) reevaluate(r *http.Request) {
	if err := s.alerts.Evaluate(r.Context()); err != nil {
		s.log.Warn("alerts: evaluate after rule change", "err", err)
	}
}

// --- notifications ---

type notifyView struct {
	Telegram  telegram.Status   `json:"telegram"`
	Quiet     alerts.Quiet      `json:"quiet"`
	Heartbeat *heartbeat.Status `json:"heartbeat,omitempty"`
	Channels  []notify.View     `json:"channels"`
	// Services are the URL schemes Shoutrrr knows; ChannelsOK is false
	// without a master key to seal channel URLs.
	Services   []string `json:"services"`
	ChannelsOK bool     `json:"channels_ok"`
}

func (s *Server) handleNotify(w http.ResponseWriter, r *http.Request) {
	q, err := alerts.LoadQuiet(r.Context(), s.store)
	if err != nil {
		s.internalError(w, "load quiet", err)
		return
	}
	v := notifyView{Quiet: q, Channels: []notify.View{}, Services: notify.Services()}
	if s.bot != nil {
		v.Telegram = s.bot.Status()
	}
	if s.heartbeat != nil {
		hb := s.heartbeat.Status()
		v.Heartbeat = &hb
	}
	if s.hub != nil {
		v.Channels, v.ChannelsOK = s.hub.List(), s.hub.Configured()
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleTelegramSave(w http.ResponseWriter, r *http.Request) {
	if s.bot == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_unavailable")
		return
	}
	var req struct {
		Token  *string `json:"token"` // absent: keep, "": remove
		ChatID string  `json:"chat_id"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	var chat int64
	if c := strings.TrimSpace(req.ChatID); c != "" {
		var err error
		if chat, err = strconv.ParseInt(c, 10, 64); err != nil || chat == 0 {
			s.writeValidation(w, i18n.T("the chat id is a whole number, negative for groups"))
			return
		}
	}
	if req.Token != nil {
		t := strings.TrimSpace(*req.Token)
		if t != "" && (len(t) > 100 || !strings.Contains(t, ":") || strings.ContainsAny(t, "/?# ")) {
			s.writeValidation(w, i18n.T("a token looks like 123456789:AA…: copy it from BotFather in full"))
			return
		}
		req.Token = &t
	}
	err := s.bot.Configure(r.Context(), req.Token, chat)
	switch {
	case errors.Is(err, telegram.ErrBadToken):
		s.writeValidation(w, i18n.T("Telegram did not accept the token"))
		return
	case errors.Is(err, telegram.ErrNoSecrets):
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
		return
	case err != nil:
		s.log.Warn("telegram: configure", "err", err)
		s.writeValidation(w, i18n.T("could not check the token: %s", err.Error()))
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "telegram_config", "chat="+req.ChatID)
	writeJSON(w, http.StatusOK, s.bot.Status())
}

func (s *Server) handleTelegramTest(w http.ResponseWriter, r *http.Request) {
	if s.bot == nil {
		writeError(w, http.StatusServiceUnavailable, "telegram_unavailable")
		return
	}
	if err := s.bot.Test(r.Context()); err != nil {
		if errors.Is(err, telegram.ErrNotConfigured) {
			s.writeValidation(w, i18n.T("set the token and chat id first"))
			return
		}
		s.writeValidation(w, i18n.T("not sent: %s", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleQuietSave(w http.ResponseWriter, r *http.Request) {
	var q alerts.Quiet
	if err := readJSON(r, &q); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	q.From, q.To, q.TZ = strings.TrimSpace(q.From), strings.TrimSpace(q.To), strings.TrimSpace(q.TZ)
	if err := q.Validate(); err != nil {
		s.writeValidation(w, err.Error())
		return
	}
	if err := alerts.SaveQuiet(r.Context(), s.store, q); err != nil {
		s.internalError(w, "save quiet", err)
		return
	}
	writeJSON(w, http.StatusOK, q)
}

// --- Shoutrrr channels ---

type channelReq struct {
	Name        string  `json:"name"`
	URL         *string `json:"url"` // absent or "" on update: keep
	MinSeverity string  `json:"min_severity"`
	Enabled     bool    `json:"enabled"`
}

func (c channelReq) input() notify.Input {
	return notify.Input{Name: c.Name, URL: c.URL, MinSeverity: c.MinSeverity, Enabled: c.Enabled}
}

// channelError writes the response for a hub error; it reports false on nil.
func (s *Server) channelError(w http.ResponseWriter, what string, err error) bool {
	var ve *notify.ValidationError
	switch {
	case err == nil:
		return false
	case errors.As(err, &ve):
		s.writeValidation(w, ve.Msg)
	case errors.Is(err, notify.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found")
	case errors.Is(err, notify.ErrNoSecrets):
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
	case errors.Is(err, notify.ErrTooMany):
		s.writeValidation(w, i18n.T("at most %d notification channels", notify.MaxChannels))
	default:
		s.internalError(w, what, err)
	}
	return true
}

func (s *Server) hubOr503(w http.ResponseWriter) bool {
	if s.hub == nil {
		writeError(w, http.StatusServiceUnavailable, "notify_unavailable")
		return false
	}
	return true
}

func (s *Server) handleChannelCreate(w http.ResponseWriter, r *http.Request) {
	if !s.hubOr503(w) {
		return
	}
	var req channelReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	v, err := s.hub.Add(r.Context(), req.input())
	if s.channelError(w, "add channel", err) {
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "notify_channel_add", fmt.Sprintf("%d %s %s", v.ID, v.Service, v.Name))
	writeJSON(w, http.StatusCreated, v)
}

func (s *Server) handleChannelUpdate(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	if !s.hubOr503(w) {
		return
	}
	var req channelReq
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	v, err := s.hub.Update(r.Context(), id, req.input())
	if s.channelError(w, "update channel", err) {
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "notify_channel_update", fmt.Sprintf("%d %s %s", v.ID, v.Service, v.Name))
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) handleChannelDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	if !s.hubOr503(w) {
		return
	}
	if s.channelError(w, "delete channel", s.hub.Delete(r.Context(), id)) {
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "notify_channel_delete", strconv.FormatInt(id, 10))
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (s *Server) handleChannelTest(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	if !s.hubOr503(w) {
		return
	}
	err := s.hub.Test(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if err != nil {
		s.writeValidation(w, i18n.T("not sent: %s", err.Error()))
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
