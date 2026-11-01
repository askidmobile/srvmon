// Package notify fans notifications out to every place the owner wants them:
// the Telegram bot (with its ack / mute buttons) and any number of Shoutrrr
// channels (email, ntfy, Discord, Gotify, Slack, Matrix, webhooks, ...).
//
// The alerts engine and every other source of notifications (agent tasks,
// HTTP checks, ...) talk to the Hub only; channels are configured in the
// panel. Channel URLs carry credentials, so they are stored sealed with the
// master key and the API shows only the service and host.
package notify

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

// settingKey holds the sealed JSON list of channels.
const settingKey = "notify_channels_enc"

var channelsAAD = []byte("srvmon/notify-channels")

// Limits.
const (
	MaxChannels = 20
	maxName     = 60
	maxURL      = 2000
	// queueSize: messages waiting per channel (its retries block the rest).
	queueSize = 100
	// attempts per message; waits between them grow (retryWait).
	attempts = 3
)

// retryWait is how long to wait before attempt i+1 (variable for tests).
var retryWait = func(i int) time.Duration { return []time.Duration{5 * time.Second, 30 * time.Second}[min(i, 1)] }

// Errors.
var (
	ErrNoSecrets = errors.New("notify: master key is not available")
	ErrNotFound  = errors.New("notify: channel not found")
	ErrTooMany   = errors.New("notify: too many channels")
)

// ValidationError is a channel the owner has to fix; its text is shown as is.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Channel is one Shoutrrr channel as stored.
type Channel struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	URL         string    `json:"url"`
	MinSeverity string    `json:"min_severity"` // crit | warn | info
	Enabled     bool      `json:"enabled"`
	CreatedAt   time.Time `json:"created_at"`
	// Gen changes whenever the channel gets a new destination (created, URL
	// changed): queued messages and send results of an older generation are
	// dropped, so they never reach a channel that replaced it.
	Gen int64 `json:"gen"`
}

// View is a channel as the panel sees it: no credentials.
type View struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Service     string     `json:"service"` // URL scheme: smtp, ntfy, discord, ...
	Target      string     `json:"target"`  // host only
	MinSeverity string     `json:"min_severity"`
	Enabled     bool       `json:"enabled"`
	LastSent    *time.Time `json:"last_sent,omitempty"`
	LastError   string     `json:"last_error,omitempty"`
}

// Input creates or changes a channel. URL nil (on update) keeps the stored
// one.
type Input struct {
	Name        string
	URL         *string
	MinSeverity string
	Enabled     bool
}

// Message is one notification in plain text.
type Message struct {
	Severity string // decides which channels get it
	Title    string // subject / title where the service has one
	Body     string
}

// Sender delivers one message to one Shoutrrr URL.
type Sender interface {
	Send(ctx context.Context, rawURL, title, body string) error
	// Check reports whether the URL names a known service with a valid
	// configuration, without sending anything.
	Check(rawURL string) error
}

// Telegram is the part of the bot the hub uses.
type Telegram interface {
	Notify(n alerts.Notification) bool
	Send(htmlText string) bool
}

// Options for New.
type Options struct {
	Store    *store.Store
	Secrets  *secret.Box
	Log      *slog.Logger
	Sender   Sender // default: Shoutrrr
	PanelURL string // public origin, for a link in messages; optional
}

type state struct {
	lastSent  *time.Time
	lastError string
}

type job struct {
	id  int64
	gen int64
	msg Message
}

// Hub implements alerts.Notifier. Create it with New and start Run.
type Hub struct {
	st     *store.Store
	box    *secret.Box
	log    *slog.Logger
	sender Sender
	panel  string
	now    func() time.Time

	mu       sync.Mutex
	tg       Telegram
	channels []Channel
	state    map[int64]*state
	queues   map[int64]chan job // per channel; a full one rejects the message
	runCtx   context.Context    // set by Run; workers start with it
	wg       sync.WaitGroup
}

// New creates the hub and loads the stored channels.
func New(ctx context.Context, o Options) (*Hub, error) {
	if o.Log == nil {
		o.Log = slog.New(slog.DiscardHandler)
	}
	if o.Sender == nil {
		o.Sender = NewShoutrrr()
	}
	h := &Hub{
		st: o.Store, box: o.Secrets, log: o.Log, sender: o.Sender, panel: strings.TrimRight(o.PanelURL, "/"),
		now: time.Now, state: map[int64]*state{}, queues: map[int64]chan job{},
	}
	v, err := h.st.GetSetting(ctx, settingKey)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return h, nil
	case err != nil:
		return nil, err
	case h.box == nil:
		h.log.Error("notify: no master key, notification channels are not loaded")
		return h, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err == nil {
		var pt []byte
		if pt, err = h.box.Open(raw, channelsAAD); err == nil {
			err = json.Unmarshal(pt, &h.channels)
		}
	}
	if err != nil {
		h.log.Error("notify: cannot read notification channels; add them again in the panel", "err", err)
		h.channels = nil
	}
	return h, nil
}

// SetTelegram plugs the Telegram bot in (nil unplugs it).
func (h *Hub) SetTelegram(t Telegram) {
	h.mu.Lock()
	h.tg = t
	h.mu.Unlock()
}

// Run delivers queued messages until ctx is done.
//
// Each channel has its own worker, so a channel delivers in order (a retried
// alert never arrives after its recovery) while a slow channel does not hold
// up the others.
func (h *Hub) Run(ctx context.Context) {
	h.mu.Lock()
	h.runCtx = ctx
	for _, q := range h.queues {
		h.startWorkerLocked(q)
	}
	h.mu.Unlock()
	<-ctx.Done()
	h.wg.Wait()
}

func (h *Hub) startWorkerLocked(q chan job) {
	ctx := h.runCtx
	h.wg.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case j, ok := <-q:
				if !ok {
					return // channel deleted
				}
				h.deliver(ctx, j)
			}
		}
	})
}

// queueLocked returns the queue of a channel, creating it (and its worker,
// once Run has started) on first use.
func (h *Hub) queueLocked(id int64) chan job {
	q, ok := h.queues[id]
	if !ok {
		q = make(chan job, queueSize)
		h.queues[id] = q
		if h.runCtx != nil {
			h.startWorkerLocked(q)
		}
	}
	return q
}

func (h *Hub) deliver(ctx context.Context, j job) {
	for i := range attempts {
		ch, ok := h.channel(j.id)
		if !ok || ch.Gen != j.gen || !ch.Enabled || severityRank(j.msg.Severity) < severityRank(ch.MinSeverity) {
			return // deleted, replaced, switched off or narrowed meanwhile
		}
		err := h.sender.Send(ctx, ch.URL, j.msg.Title, j.msg.Body)
		h.record(ch, err)
		if err == nil || ctx.Err() != nil {
			return
		}
		h.log.Warn("notify: send failed", "channel", ch.Name, "err", redact(err.Error(), ch.URL))
		if i == attempts-1 {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(retryWait(i)):
		}
	}
}

// record stores the result of a send to ch (the channel as it was when the
// send started); results for a since replaced or deleted channel are dropped.
func (h *Hub) record(ch Channel, err error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if cur, ok := h.channelLocked(ch.ID); !ok || cur.Gen != ch.Gen {
		return
	}
	st := h.state[ch.ID]
	if st == nil {
		st = &state{}
		h.state[ch.ID] = st
	}
	if err != nil {
		st.lastError = redact(err.Error(), ch.URL)
		return
	}
	now := h.now().UTC()
	st.lastSent, st.lastError = &now, ""
}

func (h *Hub) channel(id int64) (Channel, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.channelLocked(id)
}

func (h *Hub) channelLocked(id int64) (Channel, bool) {
	for _, c := range h.channels {
		if c.ID == id {
			return c, true
		}
	}
	return Channel{}, false
}

// --- sending ---

// Notify implements alerts.Notifier: Telegram gets the message with its
// buttons, Shoutrrr channels a plain text version. It reports whether any
// destination accepted it; false makes the engine retry later.
func (h *Hub) Notify(n alerts.Notification) bool {
	h.mu.Lock()
	tg := h.tg
	h.mu.Unlock()
	ok := false
	if tg != nil && tg.Notify(n) {
		ok = true
	}
	if h.Enqueue(h.alertMessage(n)) > 0 {
		ok = true
	}
	return ok
}

// Enqueue queues a plain text message for every enabled channel whose
// minimum severity it reaches and returns how many accepted it. Sources
// other than alerts (checks, the panel heartbeat, ...) may call it directly.
func (h *Hub) Enqueue(m Message) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	n := 0
	for _, c := range h.channels {
		if !c.Enabled || severityRank(m.Severity) < severityRank(c.MinSeverity) {
			continue
		}
		// The channel's own queue: only a message it has room for counts as
		// accepted, so the engine retries the rest later.
		select {
		case h.queueLocked(c.ID) <- job{id: c.ID, gen: c.Gen, msg: m}:
			n++
		default:
			h.log.Warn("notify: channel queue full, message not taken", "channel", c.Name)
		}
	}
	return n
}

// SendText sends a critical HTML message (agent task alerts) to Telegram
// as is and to the channels as plain text. It fails when nobody took it.
func (h *Hub) SendText(_ context.Context, htmlText string) error {
	h.mu.Lock()
	tg := h.tg
	h.mu.Unlock()
	ok := tg != nil && tg.Send(htmlText)
	text := HTMLToText(htmlText)
	title, _, _ := strings.Cut(text, "\n")
	if h.Enqueue(Message{Severity: store.SevCrit, Title: "srvmon · " + truncate(title, 120), Body: h.withLink(text)}) > 0 {
		ok = true
	}
	if !ok {
		return errors.New("notify: no notification channel took the message")
	}
	return nil
}

// Test sends a test message to one channel right away.
func (h *Hub) Test(ctx context.Context, id int64) error {
	ch, ok := h.channel(id)
	if !ok {
		return ErrNotFound
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	err := h.sender.Send(ctx, ch.URL, i18n.T("srvmon test"),
		h.withLink(i18n.T("Test message from srvmon: the channel “%s” works.", ch.Name)))
	h.record(ch, err)
	if err != nil {
		return errors.New(redact(err.Error(), ch.URL))
	}
	return nil
}

func sevLabel(sev string) string {
	switch sev {
	case store.SevCrit:
		return "🔴 CRIT"
	case store.SevWarn:
		return "🟡 WARN"
	}
	return "🔵 INFO"
}

func (h *Hub) location() *time.Location {
	q, err := alerts.LoadQuiet(context.Background(), h.st)
	if err != nil {
		return time.UTC
	}
	return q.Location()
}

// alertMessage renders a notification as plain text.
func (h *Hub) alertMessage(n alerts.Notification) Message {
	a := n.Alert
	where := a.ServerName
	if where == "" {
		where = "srvmon"
	}
	if n.Diagnosis != "" {
		// A follow-up to the alert sent before: the AI agent's diagnosis.
		return Message{Severity: a.Severity, Title: "🩺 " + i18n.T("AI diagnosis") + " · " + where,
			Body: h.withLink(fmt.Sprintf("#%d %s\n\n%s", a.ID, a.Message, n.Diagnosis))}
	}
	if n.Resolved {
		body := a.Message + "\n#" + fmt.Sprint(a.ID)
		if a.ResolvedAt != nil {
			body += " · " + i18n.T("lasted %s", alerts.FormatDuration(a.ResolvedAt.Sub(a.StartedAt)))
		}
		return Message{Severity: a.Severity, Title: "✅ RESOLVED · " + where, Body: h.withLink(body)}
	}
	body := fmt.Sprintf("%s\n#%d · %s", a.Message, a.ID, a.StartedAt.In(h.location()).Format("02.01 15:04"))
	return Message{Severity: a.Severity, Title: sevLabel(a.Severity) + " · " + where, Body: h.withLink(body)}
}

func (h *Hub) withLink(body string) string {
	if h.panel == "" {
		return body
	}
	return body + "\n" + h.panel + "/#/alerts"
}

var tagRE = regexp.MustCompile(`<[^>]*>`)

// urlRE matches URLs in error texts (up to a quote, space or bracket).
var urlRE = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s"'<>()\[\]]+`)

// HTMLToText turns Telegram HTML into plain text.
func HTMLToText(s string) string {
	return strings.TrimSpace(html.UnescapeString(tagRE.ReplaceAllString(s, "")))
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

func severityRank(s string) int {
	switch s {
	case store.SevCrit:
		return 3
	case store.SevWarn:
		return 2
	case store.SevInfo:
		return 1
	}
	return 0
}

// --- channels ---

// Configured reports whether the master key is there to store channels.
func (h *Hub) Configured() bool { return h.box != nil }

// List returns all channels without credentials.
func (h *Hub) List() []View {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]View, 0, len(h.channels))
	for _, c := range h.channels {
		out = append(out, h.viewLocked(c))
	}
	return out
}

// Health returns how many channels are enabled and how many of those failed
// their last send.
func (h *Hub) Health() (enabled, failing int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, c := range h.channels {
		if !c.Enabled {
			continue
		}
		enabled++
		if st := h.state[c.ID]; st != nil && st.lastError != "" {
			failing++
		}
	}
	return enabled, failing
}

func (h *Hub) viewLocked(c Channel) View {
	v := View{ID: c.ID, Name: c.Name, MinSeverity: c.MinSeverity, Enabled: c.Enabled}
	v.Service, v.Target = Describe(c.URL)
	if st := h.state[c.ID]; st != nil {
		v.LastSent, v.LastError = st.lastSent, st.lastError
	}
	return v
}

// hostIsPublic lists the services whose URL host is a server address. Others
// put tokens or webhook ids there (pushbullet://token, ifttt://key,
// discord://token@id), so their host is not shown.
var hostIsPublic = map[string]bool{
	"smtp": true, "ntfy": true, "gotify": true, "matrix": true, "mattermost": true, "rocketchat": true,
	"zulip": true, "generic": true, "mqtt": true, "mqtts": true, "signal": true,
}

// Describe returns the service (URL scheme) and, for services where it is a
// server address, the host of a channel URL; everything that may be a
// credential (user info, path, query, token hosts) is left out.
func Describe(raw string) (service, target string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return "?", ""
	}
	service = strings.ToLower(u.Scheme)
	if i := strings.IndexByte(service, '+'); i > 0 {
		service = service[:i] // generic+https → generic
	}
	if !hostIsPublic[service] {
		return service, ""
	}
	return service, u.Host
}

// redact removes the URL and its parts that may carry secrets from an error
// text (services quote the URL or the token in their errors).
func redact(msg, raw string) string {
	if raw == "" {
		return msg
	}
	msg = strings.ReplaceAll(msg, raw, "<url>")
	// Any URL in the text keeps only its scheme and host: services quote
	// their request URLs, whose path, query or user info carry the tokens.
	msg = urlRE.ReplaceAllStringFunc(msg, func(m string) string {
		if v, err := url.Parse(m); err == nil && v.Host != "" {
			if v.User == nil && (v.Path == "" || v.Path == "/") && v.RawQuery == "" {
				return m
			}
			return v.Scheme + "://" + v.Host + "/***"
		}
		return "<url>"
	})
	u, err := url.Parse(raw)
	if err != nil {
		return msg
	}
	var parts []string
	if u.User != nil {
		parts = append(parts, u.User.Username())
		if p, ok := u.User.Password(); ok {
			parts = append(parts, p)
		}
	}
	// Path and query in decoded and escaped form: services build their
	// request URL (e.g. generic+https → https) and errors quote it escaped.
	for seg := range strings.SplitSeq(u.Path, "/") {
		parts = append(parts, seg)
	}
	for seg := range strings.SplitSeq(u.EscapedPath(), "/") {
		parts = append(parts, seg)
	}
	for _, vs := range u.Query() {
		parts = append(parts, vs...)
	}
	parts = append(parts, strings.FieldsFunc(u.RawQuery, func(r rune) bool { return r == '&' || r == '=' })...)
	var secrets []string
	for _, p := range parts {
		secrets = append(secrets, p, url.PathEscape(p), url.QueryEscape(p))
	}
	// Longest first, so a secret is not cut in half by a shorter one.
	slices.SortFunc(secrets, func(a, b string) int { return len(b) - len(a) })
	for _, s := range secrets {
		// Bare (not in a URL) credential parts; one- and two-letter parts
		// would only mangle the text.
		if len(s) >= 3 {
			msg = strings.ReplaceAll(msg, s, "***")
		}
	}
	return msg
}

func (h *Hub) validate(in Input, needURL bool) (Input, error) {
	in.Name = strings.TrimSpace(in.Name)
	if in.Name == "" || utf8.RuneCountInString(in.Name) > maxName {
		return in, &ValidationError{i18n.T("a channel needs a name of up to %d characters", maxName)}
	}
	if !slices.Contains([]string{store.SevCrit, store.SevWarn, store.SevInfo}, in.MinSeverity) {
		return in, &ValidationError{i18n.T("minimum severity: crit, warn or info")}
	}
	if in.URL != nil {
		u := strings.TrimSpace(*in.URL)
		in.URL = &u
		if u == "" {
			in.URL = nil
		}
	}
	if in.URL == nil {
		if needURL {
			return in, &ValidationError{i18n.T("enter the channel URL")}
		}
		return in, nil
	}
	if len(*in.URL) > maxURL {
		return in, &ValidationError{i18n.T("the URL is too long")}
	}
	if err := h.sender.Check(*in.URL); err != nil {
		return in, &ValidationError{i18n.T("the URL is not valid: %s", redact(err.Error(), *in.URL))}
	}
	return in, nil
}

// Add creates a channel.
func (h *Hub) Add(ctx context.Context, in Input) (View, error) {
	if h.box == nil {
		return View{}, ErrNoSecrets
	}
	in, err := h.validate(in, true)
	if err != nil {
		return View{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.channels) >= MaxChannels {
		return View{}, ErrTooMany
	}
	var id int64 = 1
	for _, c := range h.channels {
		id = max(id, c.ID+1)
	}
	c := Channel{ID: id, Name: in.Name, URL: *in.URL, MinSeverity: in.MinSeverity, Enabled: in.Enabled, CreatedAt: h.now().UTC(), Gen: h.nextGenLocked()}
	next := append(slices.Clone(h.channels), c)
	if err := h.saveLocked(ctx, next); err != nil {
		return View{}, err
	}
	return h.viewLocked(c), nil
}

// Update changes a channel; a new URL clears its last error.
func (h *Hub) Update(ctx context.Context, id int64, in Input) (View, error) {
	if h.box == nil {
		return View{}, ErrNoSecrets
	}
	in, err := h.validate(in, false)
	if err != nil {
		return View{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	next := slices.Clone(h.channels)
	i := slices.IndexFunc(next, func(c Channel) bool { return c.ID == id })
	if i < 0 {
		return View{}, ErrNotFound
	}
	next[i].Name, next[i].MinSeverity, next[i].Enabled = in.Name, in.MinSeverity, in.Enabled
	if in.URL != nil {
		next[i].URL, next[i].Gen = *in.URL, h.nextGenLocked()
	}
	if err := h.saveLocked(ctx, next); err != nil {
		return View{}, err
	}
	if in.URL != nil {
		delete(h.state, id)
	}
	return h.viewLocked(next[i]), nil
}

// Delete removes a channel.
func (h *Hub) Delete(ctx context.Context, id int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	next := slices.DeleteFunc(slices.Clone(h.channels), func(c Channel) bool { return c.ID == id })
	if len(next) == len(h.channels) {
		return ErrNotFound
	}
	if h.box == nil {
		return ErrNoSecrets
	}
	if err := h.saveLocked(ctx, next); err != nil {
		return err
	}
	delete(h.state, id)
	if q, ok := h.queues[id]; ok {
		close(q) // its worker stops; queued messages were for this channel only
		delete(h.queues, id)
	}
	return nil
}

// nextGenLocked returns a generation greater than every one in use.
func (h *Hub) nextGenLocked() int64 {
	g := h.now().UnixNano()
	for _, c := range h.channels {
		g = max(g, c.Gen+1)
	}
	return g
}

func (h *Hub) saveLocked(ctx context.Context, next []Channel) error {
	pt, err := json.Marshal(next)
	if err != nil {
		return err
	}
	ct, err := h.box.Seal(pt, channelsAAD)
	if err != nil {
		return err
	}
	if err := h.st.SetSetting(ctx, settingKey, base64.StdEncoding.EncodeToString(ct)); err != nil {
		return err
	}
	h.channels = next
	return nil
}
