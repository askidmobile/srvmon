package api

import (
	"context"
	"errors"
	"html"
	"strings"

	"github.com/askidmobile/srvmon/internal/aiagent"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/store"
	"github.com/askidmobile/srvmon/internal/telegram"
)

var errNotSent = errors.New("telegram: not configured or queue full")

// wireAgentTelegram connects the AI agent and the Telegram bot: task alerts
// and confirmation requests go to the chat, its buttons decide actions and
// /ask questions the agent. Hooks set in Options take precedence.
func (s *Server) wireAgentTelegram(b *telegram.Bot) {
	if s.notify == nil {
		s.notify = func(_ context.Context, text string) error {
			if !b.Send(text) {
				return errNotSent
			}
			return nil
		}
	}
	if s.onActionPending == nil {
		s.onActionPending = func(a store.AgentAction) { b.SendAction(a.ID, actionText(a)) }
	}
	b.SetActionDecider(s.telegramDecide)
	if s.ai != nil {
		b.SetAsk(s.telegramAsk)
	}
}

func actionText(a store.AgentAction) string {
	var b strings.Builder
	if name, ok := strings.CutPrefix(a.Source, "mcp:"); ok {
		b.WriteString("🔌 " + i18n.T("<b>MCP client %s asks to run</b> #%d on <b>%s</b>:\n<code>%s</code>",
			html.EscapeString(name), a.ID, html.EscapeString(a.ServerName), html.EscapeString(a.Command)))
	} else {
		b.WriteString("🤖 " + i18n.T("<b>the agent asks to run</b> #%d on <b>%s</b>:\n<code>%s</code>", a.ID,
			html.EscapeString(a.ServerName), html.EscapeString(a.Command)))
	}
	if a.Reason != "" {
		b.WriteString("\n" + i18n.T("why: %s", html.EscapeString(a.Reason)))
	}
	b.WriteString("\n" + i18n.T("Waits for a decision for 15 minutes."))
	return b.String()
}

func (s *Server) telegramDecide(ctx context.Context, id int64, approve bool) string {
	state := store.ActionRejected
	if approve {
		state = store.ActionApproved
	}
	if !s.DecideAgentAction(ctx, id, state, "telegram") {
		return i18n.T("action #%d no longer waits for a decision", id)
	}
	if approve {
		return "✓ " + i18n.T("#%d is running", id)
	}
	return "✗ " + i18n.T("#%d rejected", id)
}

func (s *Server) telegramAsk(ctx context.Context, question string) (string, error) {
	var tr transcript
	text := "A question from the owner in Telegram. The reply goes back as a Telegram message: be brief, no tables.\n\n" + question
	err := s.ai.Run(ctx, 0, aiagent.RunRequest{Text: text}, tr.add)
	msg := tr.message()
	if msg.Error != "" {
		return "", errors.New(msg.Error)
	}
	if err != nil {
		return "", err
	}
	return truncate(strings.TrimSpace(msg.Text), 3500), nil
}
