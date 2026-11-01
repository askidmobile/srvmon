package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

// AI diagnosis of alerts: when an alert fires, the agent collects logs and
// state with read-only commands and explains the likely cause. The result is
// stored with the alert and sent as a reply to its Telegram notification.
//
// Read-only is enforced here, not by the prompt: a diagnosis run passes its
// alert id on every tool call, and toolExec refuses any command Classify
// does not consider read-only (no confirmation is asked), as well as calls
// beyond the command budget. srvmon-ai gives a diagnosis no task tools.
//
// Cost is capped: one diagnosis at a time, a short queue, the same rule on
// the same server and subject at most once per diagCooldown, and hourly and
// daily limits from the settings (manual runs count too).

const (
	settingDiagnose = "ai.diagnose"
	diagCooldown    = 6 * time.Hour
	diagRunTimeout  = 5 * time.Minute
	diagMaxCommands = 12
	diagQueue       = 4
	diagTextMax     = 3000
)

// diagSettings is the stored configuration of automatic diagnoses.
type diagSettings struct {
	Enabled bool   `json:"enabled"`
	Warn    bool   `json:"warn"` // warnings too, not only critical alerts
	PerHour int    `json:"per_hour"`
	PerDay  int    `json:"per_day"`
	Model   string `json:"model"` // "" = the default model
}

func defaultDiagSettings() diagSettings {
	return diagSettings{Enabled: true, PerHour: 4, PerDay: 20}
}

func (d diagSettings) validate() string {
	switch {
	case d.PerHour < 1 || d.PerHour > 60:
		return i18n.T("diagnoses per hour: 1–60")
	case d.PerDay < 1 || d.PerDay > 500:
		return i18n.T("diagnoses per day: 1–500")
	case d.PerHour > d.PerDay:
		return i18n.T("the hourly limit cannot exceed the daily one")
	case len(d.Model) > 200:
		return i18n.T("model id is too long")
	}
	return ""
}

func (s *Server) loadDiagSettings(ctx context.Context) diagSettings {
	d := defaultDiagSettings()
	if v, err := s.store.GetSetting(ctx, settingDiagnose); err == nil {
		_ = json.Unmarshal([]byte(v), &d)
	}
	return d
}

type diagJob struct {
	alert store.Alert
	model string
}

// diagnoser runs diagnoses one at a time and keeps the command budget of
// the running one.
type diagnoser struct {
	mu     sync.Mutex // orders limit checks with the queued row
	queue  chan diagJob
	budget sync.Map // alert id -> *diagBudget, while running
}

type diagBudget struct {
	mu   sync.Mutex
	left int
	used int
}

func newDiagnoser() *diagnoser { return &diagnoser{queue: make(chan diagJob, diagQueue)} }

var (
	errDiagOff     = errors.New("agent_disabled")
	errDiagLimit   = errors.New("limit")
	errDiagUnfit   = errors.New("not_diagnosable")
	errDiagRunning = errors.New("running")
)

// diagnosable: alerts about a server state the agent can look at.
func diagnosable(a store.Alert) bool {
	return a.ServerID > 0 && a.Kind != store.KindPaymentDue
}

// onAlertFired is the alert engine hook. It runs under the engine lock, so
// it only hands the alert over.
func (s *Server) onAlertFired(a store.Alert) {
	if s.ai == nil || !diagnosable(a) || store.EventKind(a.Kind) {
		return
	}
	go func() {
		if _, err := s.queueDiagnosis(context.Background(), a, "auto"); err != nil && !errors.Is(err, errDiagOff) {
			s.log.Info("ai diagnosis not started", "alert", a.ID, "reason", err)
		}
	}()
}

// queueDiagnosis checks the limits and queues a diagnosis. Automatic ones
// that the settings do not want return errDiagOff without leaving a trace;
// those over a limit leave a skipped row, so the alert shows why, unless
// the alert already has a diagnosis (a refused rerun keeps it).
func (s *Server) queueDiagnosis(ctx context.Context, a store.Alert, trigger string) (store.Diagnosis, error) {
	if s.ai == nil {
		return store.Diagnosis{}, errDiagOff
	}
	if !diagnosable(a) {
		return store.Diagnosis{}, errDiagUnfit
	}
	set := s.loadDiagSettings(ctx)
	auto := trigger == "auto"
	if auto {
		if !set.Enabled || (a.Severity != store.SevCrit && !(set.Warn && a.Severity == store.SevWarn)) {
			return store.Diagnosis{}, errDiagOff
		}
	}
	d := s.diag
	d.mu.Lock()
	defer d.mu.Unlock()
	now := s.now().UTC()
	prev, err := s.store.GetDiagnosis(ctx, a.ID)
	hasPrev := err == nil && prev.State != store.DiagSkipped
	if err == nil && (prev.State == store.DiagQueued || prev.State == store.DiagRunning) {
		return prev, errDiagRunning
	}
	if auto {
		last, err := s.store.LastAutoDiagnosis(ctx, store.SilenceKey{RuleID: a.RuleID, ServerID: a.ServerID, Subject: a.Subject})
		if err != nil {
			return store.Diagnosis{}, err
		}
		if !last.IsZero() && now.Sub(last) < diagCooldown {
			return store.Diagnosis{}, errDiagOff // flapping: one diagnosis is enough
		}
	}
	diag := store.Diagnosis{AlertID: a.ID, State: store.DiagQueued, Trigger: trigger, StartedAt: now}
	skip := func(why string) (store.Diagnosis, error) {
		diag.State, diag.Text, diag.FinishedAt = store.DiagSkipped, why, &now
		if !hasPrev {
			if err := s.store.PutDiagnosis(ctx, diag); err != nil {
				return diag, err
			}
		}
		return diag, errDiagLimit
	}
	hour, err := s.store.CountDiagnosesSince(ctx, now.Add(-time.Hour))
	if err != nil {
		return diag, err
	}
	day, err := s.store.CountDiagnosesSince(ctx, now.Add(-24*time.Hour))
	if err != nil {
		return diag, err
	}
	switch {
	case hour >= set.PerHour:
		return skip(i18n.T("skipped: the limit of %d diagnoses per hour is reached", set.PerHour))
	case day >= set.PerDay:
		return skip(i18n.T("skipped: the limit of %d diagnoses per day is reached", set.PerDay))
	case len(d.queue) >= cap(d.queue):
		// Only queueDiagnosis sends, under d.mu: the send below cannot block.
		return skip(i18n.T("skipped: too many diagnoses at once"))
	}
	if err := s.store.RecordDiagnosisRun(ctx, a, trigger, now); err != nil {
		return diag, err
	}
	if err := s.store.PutDiagnosis(ctx, diag); err != nil {
		return diag, err
	}
	d.queue <- diagJob{alert: a, model: set.Model}
	return diag, nil
}

// runDiagnoses works the queue until ctx ends.
func (s *Server) runDiagnoses(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case j := <-s.diag.queue:
			s.diagnose(ctx, j)
		}
	}
}

func diagPrompt(a store.Alert) string {
	var b strings.Builder
	fmt.Fprintf(&b, "An alert fired in srvmon. Find its likely cause.\n\n")
	fmt.Fprintf(&b, "Alert #%d, severity %s, rule kind %s, server %q", a.ID, a.Severity, a.Kind, a.ServerName)
	if a.Subject != "" {
		fmt.Fprintf(&b, ", subject %q", a.Subject)
	}
	fmt.Fprintf(&b, ", started %s UTC.\nMessage: %s\n\n", a.StartedAt.UTC().Format("2006-01-02 15:04"), a.Message)
	b.WriteString("This is a READ-ONLY diagnosis: commands that change a server are refused, nobody confirms them. ")
	fmt.Fprintf(&b, "You have at most %d commands: start with srvmon_server, then targeted reads ", diagMaxCommands)
	b.WriteString("(docker logs --tail 100 <name>, docker inspect, docker ps -a, df -h, free -m, journalctl -n 100, dmesg | tail, ps aux --sort=-%cpu | head and so on).\n")
	switch a.Kind {
	case store.KindAgentSilent:
		b.WriteString("The srvmon agent stopped reporting: check whether ssh works, the srvmon-agent container and its logs, the network. If ssh fails, say the server may be down or unreachable.\n")
	case store.KindContainerDown, store.KindContainerRestarts:
		b.WriteString("Look at the container's last logs, exit code, OOMKilled, restart count and health check.\n")
	case store.KindDisk:
		b.WriteString("Find what takes the space (du -xh --max-depth=2 on the mount, docker system df, big logs).\n")
	case store.KindCPU, store.KindMem:
		b.WriteString("Find the processes or containers behind the load (ps, docker stats --no-stream) and OOM traces.\n")
	}
	b.WriteString("\nReply in 3–8 short lines of plain text, no tables, no headings:\n" +
		"1. the likely cause; 2. the evidence (a key log line or number); 3. a fix the owner can apply, as a command in `code` (you do not run it).\n" +
		"If the cause is unclear, say so and what to check next. Do not repeat secrets from the output.")
	return b.String()
}

func (s *Server) diagnose(ctx context.Context, j diagJob) {
	a := j.alert
	b := &diagBudget{left: diagMaxCommands}
	s.diag.budget.Store(a.ID, b)
	defer s.diag.budget.Delete(a.ID)
	_ = s.store.SetDiagnosisState(ctx, a.ID, store.DiagRunning)
	s.log.Info("ai diagnosis start", "alert", a.ID, "server", a.ServerName, "kind", a.Kind)

	rctx, cancel := context.WithTimeout(ctx, diagRunTimeout)
	defer cancel()
	var tr transcript
	err := s.ai.Run(rctx, 0, aiagent.RunRequest{Text: diagPrompt(a), DiagnoseID: a.ID, Model: j.model,
		Server: a.ServerName}, tr.add)
	msg := tr.message()
	state, text := store.DiagDone, strings.TrimSpace(msg.Text)
	if err != nil || msg.Error != "" {
		e := msg.Error
		switch {
		case e != "":
		case errors.Is(err, context.DeadlineExceeded):
			e = i18n.T("the reply timed out")
		default:
			e = err.Error()
		}
		state = store.DiagError
		text = strings.TrimSpace(text + "\n" + i18n.T("error: %s", e))
	}
	if text == "" {
		state, text = store.DiagError, i18n.T("the agent gave no answer")
	}
	text = truncate(text, diagTextMax)
	b.mu.Lock()
	used := b.used
	b.mu.Unlock()
	if err := s.store.FinishDiagnosis(context.Background(), a.ID, state, text, used, s.now()); err != nil {
		s.log.Error("store ai diagnosis", "err", err)
	}
	_ = s.store.Audit(context.Background(), "agent", "agent_diagnosis", fmt.Sprintf("#%d %s %s", a.ID, a.ServerName, state))
	s.log.Info("ai diagnosis done", "alert", a.ID, "state", state, "commands", used)
	if state == store.DiagDone {
		// Where the alert's notification went. When internal/notify lands,
		// every channel of the alert gets it through the same Notifier.
		s.alerts.NotifyDiagnosis(context.Background(), a.ID, text)
	}
}

// diagExecCheck is toolExec's gate for diagnosis runs: "" lets the command
// run, anything else is the note of a refusal.
func (s *Server) diagExecCheck(alertID int64, readOnly bool) string {
	v, ok := s.diag.budget.Load(alertID)
	if !ok {
		return "this diagnosis is not running any more"
	}
	if !readOnly {
		return "refused: this is a read-only diagnosis, commands that change a server are not allowed. " +
			"Do not try another form of it; describe the fix for the owner instead"
	}
	b := v.(*diagBudget)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.left <= 0 {
		return fmt.Sprintf("refused: the budget of %d commands is used up; answer with what you found", diagMaxCommands)
	}
	b.left--
	b.used++
	return ""
}

// --- HTTP ---

func (s *Server) handleDiagnoseSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.loadDiagSettings(r.Context()))
}

func (s *Server) handleDiagnoseSave(w http.ResponseWriter, r *http.Request) {
	d := s.loadDiagSettings(r.Context())
	if err := readJSON(r, &d); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	d.Model = strings.TrimSpace(d.Model)
	if msg := d.validate(); msg != "" {
		s.writeValidation(w, msg)
		return
	}
	if d.Model != "" {
		if a, ok, _ := s.loadAISettings(r.Context()); ok && !slices.Contains(a.Models, d.Model) && a.Model != d.Model {
			s.writeValidation(w, i18n.T("pick a model from the list in the AI settings"))
			return
		}
	}
	raw, _ := json.Marshal(d)
	if err := s.store.SetSetting(r.Context(), settingDiagnose, string(raw)); err != nil {
		s.internalError(w, "save diagnose settings", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "ai_diagnose_settings", string(raw))
	writeJSON(w, http.StatusOK, d)
}

func (s *Server) handleAlertDiagnose(w http.ResponseWriter, r *http.Request) {
	if !s.agentEnabled(w) {
		return
	}
	id, ok := pathID(r)
	if !ok {
		writeError(w, http.StatusBadRequest, "bad_id")
		return
	}
	a, err := s.store.GetAlert(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	} else if err != nil {
		s.internalError(w, "get alert", err)
		return
	}
	d, err := s.queueDiagnosis(r.Context(), a, "manual")
	switch {
	case errors.Is(err, errDiagUnfit):
		s.writeValidation(w, i18n.T("this alert is not about a server state: nothing to diagnose"))
		return
	case errors.Is(err, errDiagRunning):
		writeJSON(w, http.StatusOK, d)
		return
	case errors.Is(err, errDiagLimit):
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "limit", "message": d.Text})
		return
	case err != nil:
		s.internalError(w, "queue diagnosis", err)
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "agent_diagnosis_manual", fmt.Sprintf("#%d", a.ID))
	writeJSON(w, http.StatusOK, d)
}
