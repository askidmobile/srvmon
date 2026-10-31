package api

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strings"

	"github.com/askidmobile/srvmon/internal/aiagent"
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
	fmt.Fprintf(&b, "🤖 <b>агент просит выполнить</b> #%d на <b>%s</b>:\n<code>%s</code>", a.ID,
		html.EscapeString(a.ServerName), html.EscapeString(a.Command))
	if a.Reason != "" {
		fmt.Fprintf(&b, "\nзачем: %s", html.EscapeString(a.Reason))
	}
	b.WriteString("\nЖдёт решения 15 минут.")
	return b.String()
}

func (s *Server) telegramDecide(ctx context.Context, id int64, approve bool) string {
	state := store.ActionRejected
	if approve {
		state = store.ActionApproved
	}
	if !s.DecideAgentAction(ctx, id, state, "telegram") {
		return fmt.Sprintf("действие #%d уже не ждёт решения", id)
	}
	if approve {
		return fmt.Sprintf("✓ #%d выполняется", id)
	}
	return fmt.Sprintf("✗ #%d отклонено", id)
}

func (s *Server) telegramAsk(ctx context.Context, question string) (string, error) {
	var tr transcript
	text := "Вопрос владельца из Telegram. Ответ уйдёт сообщением в Telegram: будь краток, без таблиц.\n\n" + question
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
