package api

import (
	"context"
	"fmt"
	"html"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	taskTick       = 30 * time.Second
	taskRunTimeout = 20 * time.Minute
	taskResultMax  = 8 << 10
)

// RunAgentScheduler starts due tasks until ctx ends. Call once from main.
func (s *Server) RunAgentScheduler(ctx context.Context) {
	_ = s.store.ResetRunningAgentTasks(ctx)
	_ = s.store.ExpirePendingAgentActions(ctx)
	if s.ai == nil {
		return
	}
	t := time.NewTicker(taskTick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.startDueTasks(ctx)
		}
	}
}

func (s *Server) startDueTasks(ctx context.Context) {
	tasks, err := s.store.ListAgentTasks(ctx)
	if err != nil {
		s.log.Error("list agent tasks", "err", err)
		return
	}
	now := s.now()
	for _, t := range tasks {
		if !t.Enabled || t.NextRunAt == nil || t.NextRunAt.After(now) {
			continue
		}
		s.startTask(t, "schedule")
	}
}

// startTask claims the task and runs it in the background. It returns false
// when the task is already running.
func (s *Server) startTask(t store.AgentTask, trigger string) bool {
	var next *time.Time
	if sched, err := aiagent.ParseSchedule(t.Schedule); err == nil {
		// Next run counts from now: a long outage does not replay missed runs.
		if n := sched.Next(s.now()); !n.IsZero() {
			next = &n
		}
	}
	if trigger == "manual" {
		next = t.NextRunAt
	}
	ok, err := s.store.ClaimAgentTask(context.Background(), t.ID, next)
	if err != nil || !ok {
		return false
	}
	go s.runTask(t, trigger)
	return true
}

func taskPrompt(t store.AgentTask) string {
	var b strings.Builder
	fmt.Fprintf(&b, "This is a run of scheduled task #%d %q (%s, run at %s UTC).\n", t.ID, t.Name, t.Schedule,
		time.Now().UTC().Format("2006-01-02 15:04"))
	if len(t.Servers) > 0 {
		fmt.Fprintf(&b, "Task servers: %s.\n", strings.Join(t.Servers, ", "))
	} else {
		b.WriteString("Task servers: all srvmon servers.\n")
	}
	b.WriteString("The reply is saved as the task result")
	if t.Notify == "telegram" {
		b.WriteString(" and goes to Telegram if it starts with a line ALERT")
	}
	b.WriteString(". Be brief: the outcome in 1–5 lines. If all is well, say so in one line.\n\n")
	b.WriteString(t.Prompt)
	return b.String()
}

func (s *Server) runTask(t store.AgentTask, trigger string) {
	ctx, cancel := context.WithTimeout(context.Background(), taskRunTimeout)
	defer cancel()
	s.log.Info("agent task start", "task", t.ID, "name", t.Name, "trigger", trigger)
	var tr transcript
	err := s.ai.Run(ctx, 0, aiagent.RunRequest{Text: taskPrompt(t), TaskID: t.ID}, tr.add)
	msg := tr.message()
	status, result := "ok", strings.TrimSpace(msg.Text)
	if err != nil || msg.Error != "" {
		status = "error"
		e := msg.Error
		if e == "" {
			e = err.Error()
		}
		result = strings.TrimSpace(result + "\n" + i18n.T("error: %s", e))
	}
	result = truncate(result, taskResultMax)
	if err := s.store.FinishAgentTask(context.Background(), t.ID, status, result); err != nil {
		s.log.Error("finish agent task", "err", err)
	}
	_ = s.store.Audit(context.Background(), "agent", "agent_task_run", fmt.Sprintf("%s %s", t.Name, status))
	if t.Notify == "telegram" && s.notify != nil && (status == "error" || strings.HasPrefix(result, "ALERT")) {
		text := "🤖 " + i18n.T("<b>task #%d %s</b>\n%s", t.ID, html.EscapeString(t.Name), html.EscapeString(result))
		if err := s.notify(context.Background(), text); err != nil {
			s.log.Warn("agent task notify", "err", err)
		}
	}
}
