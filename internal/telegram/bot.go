// Package telegram is the srvmon Telegram bot: it sends alert notifications
// to one allowed chat and answers status commands from it. It uses long
// polling, so the monitoring server needs no inbound webhook.
package telegram

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

// Setting keys.
const (
	keyToken  = "tg_token_enc" // sealed with the master key
	keyChatID = "tg_chat_id"
)

var tokenAAD = []byte("srvmon/telegram-token")

// DefaultAPI is the Bot API base URL.
const DefaultAPI = "https://api.telegram.org"

// ErrNoSecrets is returned when there is no master key to seal the token.
var ErrNoSecrets = errors.New("telegram: master key is not available")

// ErrBadToken is returned when Telegram rejects the bot token.
var ErrBadToken = errors.New("telegram: token rejected")

// ErrNotConfigured is returned by Test without a token or chat.
var ErrNotConfigured = errors.New("telegram: bot is not configured")

// Chat is a chat that wrote to the bot but is not the allowed one; the
// panel offers it as the chat id to use.
type Chat struct {
	ID    int64     `json:"id,string"`
	Title string    `json:"title"`
	At    time.Time `json:"at"`
}

// Status is what the settings screen shows.
type Status struct {
	Configured  bool       `json:"configured"` // token stored
	ChatID      string     `json:"chat_id"`
	Connected   bool       `json:"connected"` // last poll succeeded
	BotName     string     `json:"bot_name,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
	LastMessage *time.Time `json:"last_message,omitempty"` // last command from the owner
	LastSent    *time.Time `json:"last_sent,omitempty"`
	PendingChat *Chat      `json:"pending_chat,omitempty"`
}

// AskFunc answers a free-form question (/ask); stage 5 plugs the AI agent in.
type AskFunc func(ctx context.Context, question string) (string, error)

// ActionFunc approves (or rejects) a command the AI agent asked to run; it
// returns the answer shown to the owner.
type ActionFunc func(ctx context.Context, id int64, approve bool) string

// askTimeout bounds one /ask: the agent may wait for a confirmation.
const askTimeout = 20 * time.Minute

// Options for New.
type Options struct {
	Store   *store.Store
	Secrets *secret.Box
	Engine  *alerts.Engine
	Latest  *metrics.Latest
	Log     *slog.Logger
	API     string       // Bot API base URL; default DefaultAPI
	Client  *http.Client // default: 70 s timeout (long polling waits 50 s)
}

// Bot is the Telegram bot. Run starts it; it idles until a token is set.
type Bot struct {
	api    string
	client *http.Client
	st     *store.Store
	box    *secret.Box
	engine *alerts.Engine
	latest *metrics.Latest
	log    *slog.Logger
	now    func() time.Time

	mu      sync.Mutex
	token   string
	chatID  int64
	status  Status
	ask     AskFunc
	decide  ActionFunc
	reload  chan struct{}
	out     chan outMsg
	started time.Time
}

type outMsg struct {
	text     string
	keyboard [][]button
}

type button struct {
	Text string `json:"text"`
	Data string `json:"callback_data"`
}

// New creates the bot and loads its settings.
func New(ctx context.Context, o Options) (*Bot, error) {
	if o.API == "" {
		o.API = DefaultAPI
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 70 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	b := &Bot{
		api: strings.TrimRight(o.API, "/"), client: o.Client, st: o.Store, box: o.Secrets, engine: o.Engine,
		latest: o.Latest, log: o.Log, now: time.Now,
		reload: make(chan struct{}, 1), out: make(chan outMsg, 100),
	}
	b.started = b.now()
	if err := b.load(ctx); err != nil {
		return nil, err
	}
	return b, nil
}

func (b *Bot) load(ctx context.Context) error {
	var token string
	if v, err := b.st.GetSetting(ctx, keyToken); err == nil && b.box != nil {
		raw, err := base64.StdEncoding.DecodeString(v)
		if err == nil {
			pt, err := b.box.Open(raw, tokenAAD)
			if err != nil {
				b.log.Error("telegram: cannot decrypt the bot token; set it again in the panel")
			} else {
				token = string(pt)
			}
		}
	} else if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	var chat int64
	if v, err := b.st.GetSetting(ctx, keyChatID); err == nil {
		chat, _ = strconv.ParseInt(v, 10, 64)
	} else if !errors.Is(err, store.ErrNotFound) {
		return err
	}
	b.mu.Lock()
	b.token, b.chatID = token, chat
	b.status.Configured = token != ""
	b.status.ChatID = ""
	if chat != 0 {
		b.status.ChatID = strconv.FormatInt(chat, 10)
	}
	b.mu.Unlock()
	return nil
}

// SetAsk sets the /ask handler.
func (b *Bot) SetAsk(f AskFunc) {
	b.mu.Lock()
	b.ask = f
	b.mu.Unlock()
}

// SetActionDecider sets the handler of the agent's approve / reject buttons.
func (b *Bot) SetActionDecider(f ActionFunc) {
	b.mu.Lock()
	b.decide = f
	b.mu.Unlock()
}

// SendAction queues a confirmation request (HTML text) with approve and
// reject buttons for agent action id.
func (b *Bot) SendAction(id int64, text string) bool {
	token, chat := b.creds()
	if token == "" || chat == 0 {
		return false
	}
	ids := strconv.FormatInt(id, 10)
	kb := [][]button{{{Text: "✓ выполнить", Data: "act:" + ids + ":ok"}, {Text: "✗ отклонить", Data: "act:" + ids + ":no"}}}
	select {
	case b.out <- outMsg{text: text, keyboard: kb}:
		return true
	default:
		return false
	}
}

// Status returns the current bot status.
func (b *Bot) Status() Status {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.status
	if s.PendingChat != nil && strconv.FormatInt(s.PendingChat.ID, 10) == s.ChatID {
		s.PendingChat = nil
	}
	return s
}

func (b *Bot) creds() (string, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.token, b.chatID
}

// Configure stores a new token (nil keeps the current one, "" removes it)
// and the allowed chat id (0: none). A new token is checked with getMe.
func (b *Bot) Configure(ctx context.Context, token *string, chatID int64) error {
	if token != nil && *token != "" {
		if b.box == nil {
			return ErrNoSecrets
		}
		if _, err := b.getMe(ctx, *token); err != nil {
			return err
		}
		sealed, err := b.box.Seal([]byte(*token), tokenAAD)
		if err != nil {
			return err
		}
		if err := b.st.SetSetting(ctx, keyToken, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			return err
		}
	} else if token != nil {
		if err := b.st.SetSetting(ctx, keyToken, ""); err != nil {
			return err
		}
	}
	chat := ""
	if chatID != 0 {
		chat = strconv.FormatInt(chatID, 10)
	}
	if err := b.st.SetSetting(ctx, keyChatID, chat); err != nil {
		return err
	}
	if err := b.load(ctx); err != nil {
		return err
	}
	b.mu.Lock()
	if token != nil {
		b.status.Connected, b.status.BotName, b.status.LastError = false, "", ""
	}
	b.mu.Unlock()
	select {
	case b.reload <- struct{}{}:
	default:
	}
	return nil
}

// Run polls Telegram and delivers notifications until ctx is done.
func (b *Bot) Run(ctx context.Context) {
	go b.sender(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		token, _ := b.creds()
		if token == "" {
			select {
			case <-ctx.Done():
				return
			case <-b.reload:
			}
			continue
		}
		pollCtx, cancel := context.WithCancel(ctx)
		reloaded := make(chan struct{})
		go func() {
			select {
			case <-b.reload:
				close(reloaded)
				cancel()
			case <-pollCtx.Done():
			}
		}()
		err := b.poll(pollCtx, token)
		cancel()
		select {
		case <-reloaded:
			backoff = time.Second
			continue
		default:
		}
		if ctx.Err() != nil {
			return
		}
		b.setError(err)
		b.log.Warn("telegram: polling stopped", "err", err, "retry_in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-b.reload:
			backoff = time.Second
		case <-time.After(backoff):
			backoff = min(backoff*2, time.Minute)
		}
	}
}

func (b *Bot) setError(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.status.Connected = false
	if err != nil {
		b.status.LastError = err.Error()
	}
}

var commands = []map[string]string{
	{"command": "status", "description": "сводка по серверам"},
	{"command": "server", "description": "сервер подробно: /server <имя>"},
	{"command": "alerts", "description": "активные алерты"},
	{"command": "ack", "description": "подтвердить алерт: /ack <id>"},
	{"command": "bills", "description": "ближайшие оплаты серверов"},
	{"command": "ask", "description": "вопрос AI-агенту"},
	{"command": "help", "description": "список команд"},
}

func (b *Bot) poll(ctx context.Context, token string) error {
	me, err := b.getMe(ctx, token)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.status.Connected, b.status.BotName, b.status.LastError = true, me, ""
	b.mu.Unlock()
	_ = b.call(ctx, token, "setMyCommands", map[string]any{"commands": commands}, nil)
	var offset int64
	for {
		var updates []update
		err := b.call(ctx, token, "getUpdates", map[string]any{
			"offset": offset, "timeout": 50, "allowed_updates": []string{"message", "callback_query"},
		}, &updates)
		if err != nil {
			return err
		}
		b.mu.Lock()
		b.status.Connected, b.status.LastError = true, ""
		b.mu.Unlock()
		for _, u := range updates {
			offset = max(offset, u.ID+1)
			b.handle(ctx, token, u)
		}
	}
}

type tgUser struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

type tgChat struct {
	ID       int64  `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Username string `json:"username"`
	First    string `json:"first_name"`
}

type tgMessage struct {
	ID   int64   `json:"message_id"`
	Date int64   `json:"date"`
	Chat tgChat  `json:"chat"`
	From *tgUser `json:"from"`
	Text string  `json:"text"`
}

type update struct {
	ID       int64      `json:"update_id"`
	Message  *tgMessage `json:"message"`
	Callback *struct {
		ID      string     `json:"id"`
		Data    string     `json:"data"`
		Message *tgMessage `json:"message"`
	} `json:"callback_query"`
}

func chatTitle(c tgChat) string {
	switch {
	case c.Title != "":
		return c.Title
	case c.Username != "":
		return "@" + c.Username
	}
	return c.First
}

func (b *Bot) handle(ctx context.Context, token string, u update) {
	_, allowed := b.creds()
	switch {
	case u.Message != nil:
		m := u.Message
		if m.Chat.ID != allowed {
			b.unknownChat(ctx, token, allowed, m)
			return
		}
		if time.Unix(m.Date, 0).Before(b.started.Add(-time.Minute)) {
			return // backlog from before our start: do not run stale commands
		}
		now := b.now().UTC()
		b.mu.Lock()
		b.status.LastMessage = &now
		b.mu.Unlock()
		reply, kb := b.command(ctx, m.Text)
		if reply != "" {
			_ = b.send(ctx, token, allowed, reply, kb)
		}
	case u.Callback != nil:
		cb := u.Callback
		if cb.Message == nil || cb.Message.Chat.ID != allowed {
			_ = b.call(ctx, token, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID}, nil)
			return
		}
		answer := b.callback(ctx, cb.Data)
		_ = b.call(ctx, token, "answerCallbackQuery", map[string]any{"callback_query_id": cb.ID, "text": answer}, nil)
		_ = b.call(ctx, token, "editMessageReplyMarkup", map[string]any{
			"chat_id": allowed, "message_id": cb.Message.ID, "reply_markup": map[string]any{"inline_keyboard": [][]button{}},
		}, nil)
		_ = b.send(ctx, token, allowed, html.EscapeString(answer), nil)
	}
}

// unknownChat remembers a chat that is not allowed, so the panel can offer
// its id. The bot answers it only while no chat is configured yet.
func (b *Bot) unknownChat(ctx context.Context, token string, allowed int64, m *tgMessage) {
	b.mu.Lock()
	b.status.PendingChat = &Chat{ID: m.Chat.ID, Title: chatTitle(m.Chat), At: b.now().UTC()}
	b.mu.Unlock()
	if allowed != 0 {
		return
	}
	text := fmt.Sprintf("Это бот srvmon. Chat id этого чата: <code>%d</code>.\n"+
		"Откройте в панели раздел «алерты» → telegram-бот и нажмите «использовать этот чат».", m.Chat.ID)
	_ = b.send(ctx, token, m.Chat.ID, text, nil)
}

// Notify queues an alert notification. It returns false when the bot is
// not configured or the queue is full.
func (b *Bot) Notify(n alerts.Notification) bool {
	token, chat := b.creds()
	if token == "" || chat == 0 {
		return false
	}
	text, kb := b.alertText(n)
	select {
	case b.out <- outMsg{text: text, keyboard: kb}:
		return true
	default:
		b.log.Warn("telegram: queue full, notification dropped", "alert", n.Alert.ID)
		return false
	}
}

// Send queues a plain HTML message for the allowed chat.
func (b *Bot) Send(text string) bool {
	token, chat := b.creds()
	if token == "" || chat == 0 {
		return false
	}
	select {
	case b.out <- outMsg{text: text}:
		return true
	default:
		return false
	}
}

func (b *Bot) sender(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-b.out:
			for attempt := 0; attempt < 3; attempt++ {
				token, chat := b.creds()
				if token == "" || chat == 0 {
					break
				}
				err := b.send(ctx, token, chat, m.text, m.keyboard)
				if err == nil || ctx.Err() != nil {
					break
				}
				b.log.Warn("telegram: send failed", "err", err)
				wait := 5 * time.Second
				var re *retryError
				if errors.As(err, &re) {
					wait = re.after
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(wait):
				}
			}
		}
	}
}

// Test sends a test message right away.
func (b *Bot) Test(ctx context.Context) error {
	token, chat := b.creds()
	if token == "" || chat == 0 {
		return ErrNotConfigured
	}
	return b.send(ctx, token, chat, "✅ <b>srvmon</b>: тестовое сообщение. Уведомления будут приходить сюда.", nil)
}

func (b *Bot) send(ctx context.Context, token string, chat int64, text string, kb [][]button) error {
	req := map[string]any{"chat_id": chat, "link_preview_options": map[string]bool{"is_disabled": true}}
	if len(text) > 4000 {
		// Cutting may break a tag or an entity: send it as plain text.
		req["text"] = truncate(html.UnescapeString(stripTags(text)), 4000)
	} else {
		req["text"], req["parse_mode"] = text, "HTML"
	}
	if len(kb) > 0 {
		req["reply_markup"] = map[string]any{"inline_keyboard": kb}
	}
	if err := b.call(ctx, token, "sendMessage", req, nil); err != nil {
		return err
	}
	now := b.now().UTC()
	b.mu.Lock()
	b.status.LastSent = &now
	b.mu.Unlock()
	return nil
}

// truncate cuts s to at most n bytes on a rune boundary, adding "…".
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n - len("…")
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + "…"
}

func stripTags(s string) string {
	var out strings.Builder
	in := false
	for _, r := range s {
		switch {
		case r == '<':
			in = true
		case r == '>':
			in = false
		case !in:
			out.WriteRune(r)
		}
	}
	return out.String()
}

type retryError struct {
	after time.Duration
	desc  string
}

func (e *retryError) Error() string { return e.desc }

func (b *Bot) getMe(ctx context.Context, token string) (string, error) {
	var me tgUser
	if err := b.call(ctx, token, "getMe", nil, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}

// call invokes a Bot API method. The token is part of the URL, so errors
// never include the URL.
func (b *Bot) call(ctx context.Context, token, method string, params any, out any) error {
	var body io.Reader = http.NoBody
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.api+"/bot"+token+"/"+method, body)
	if err != nil {
		return fmt.Errorf("telegram %s: bad request", method)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("telegram %s: нет связи с Bot API", method)
	}
	defer resp.Body.Close()
	var res struct {
		OK          bool            `json:"ok"`
		Result      json.RawMessage `json:"result"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Parameters  struct {
			RetryAfter int `json:"retry_after"`
		} `json:"parameters"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&res); err != nil {
		return fmt.Errorf("telegram %s: %s", method, resp.Status)
	}
	if !res.OK {
		switch {
		case res.ErrorCode == http.StatusUnauthorized || res.ErrorCode == http.StatusNotFound:
			return ErrBadToken
		case res.ErrorCode == http.StatusTooManyRequests:
			return &retryError{after: time.Duration(max(res.Parameters.RetryAfter, 1)) * time.Second,
				desc: "telegram " + method + ": " + res.Description}
		}
		return fmt.Errorf("telegram %s: %s", method, res.Description)
	}
	if out != nil {
		return json.Unmarshal(res.Result, out)
	}
	return nil
}
