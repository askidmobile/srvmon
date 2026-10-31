package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/alerts"
	"github.com/askidmobile/srvmon/internal/store"
)

// offlineAfter matches the panel: no push for this long = offline.
const offlineAfter = 2 * time.Minute

var esc = html.EscapeString

const helpText = `<b>srvmon</b> — команды:
/status — сводка по серверам
/server &lt;имя&gt; — сервер подробно
/alerts — активные алерты
/ack &lt;id&gt; — подтвердить алерт
/bills — ближайшие оплаты
/ask &lt;вопрос&gt; — вопрос AI-агенту`

func sevIcon(sev string) string {
	switch sev {
	case store.SevCrit:
		return "🔴 CRIT"
	case store.SevWarn:
		return "🟡 WARN"
	}
	return "🔵 INFO"
}

func (b *Bot) loc(ctx context.Context) *time.Location {
	q, err := alerts.LoadQuiet(ctx, b.st)
	if err != nil {
		return time.UTC
	}
	return q.Location()
}

// alertText renders a notification with ack / silence buttons.
func (b *Bot) alertText(n alerts.Notification) (string, [][]button) {
	a := n.Alert
	loc := b.loc(context.Background())
	where := esc(a.ServerName)
	if where == "" {
		where = "srvmon"
	}
	if n.Resolved {
		dur := ""
		if a.ResolvedAt != nil {
			dur = " · длилось " + alerts.FormatDuration(a.ResolvedAt.Sub(a.StartedAt))
		}
		return fmt.Sprintf("✅ <b>RESOLVED</b> · %s\n%s\n<i>#%d%s</i>", where, esc(a.Message), a.ID, dur), nil
	}
	text := fmt.Sprintf("%s · <b>%s</b>\n%s\n<i>#%d · %s</i>", sevIcon(a.Severity), where, esc(a.Message), a.ID,
		a.StartedAt.In(loc).Format("02.01 15:04"))
	if a.State == store.AlertEvent {
		return text, nil
	}
	id := strconv.FormatInt(a.ID, 10)
	return text, [][]button{{{Text: "✓ ack", Data: "ack:" + id}, {Text: "🔕 тишина 1ч", Data: "sil:" + id}}}
}

func (b *Bot) callback(ctx context.Context, data string) string {
	if rest, ok := strings.CutPrefix(data, "act:"); ok {
		return b.agentAction(ctx, rest)
	}
	action, idStr, _ := strings.Cut(data, ":")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return "неизвестная кнопка"
	}
	switch action {
	case "ack":
		return b.ack(ctx, id)
	case "sil":
		a, until, err := b.engine.Silence(ctx, id, time.Hour)
		if err != nil {
			return "алерт #" + idStr + " не найден"
		}
		return fmt.Sprintf("🔕 #%d %s: тишина до %s", a.ID, a.ServerName, until.In(b.loc(ctx)).Format("15:04"))
	}
	return "неизвестная кнопка"
}

// agentAction handles "<id>:ok|no" from the agent's confirmation buttons.
func (b *Bot) agentAction(ctx context.Context, data string) string {
	idStr, verdict, _ := strings.Cut(data, ":")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || (verdict != "ok" && verdict != "no") {
		return "неизвестная кнопка"
	}
	b.mu.Lock()
	decide := b.decide
	b.mu.Unlock()
	if decide == nil {
		return "AI-агент не подключён"
	}
	return decide(ctx, id, verdict == "ok")
}

func (b *Bot) ack(ctx context.Context, id int64) string {
	a, err := b.engine.Ack(ctx, id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return fmt.Sprintf("алерт #%d не найден", id)
	case err != nil:
		return "ошибка: " + err.Error()
	case a.State == store.AlertAcked:
		return fmt.Sprintf("✓ #%d %s подтверждён", a.ID, a.ServerName)
	}
	return fmt.Sprintf("#%d уже %s", a.ID, a.State)
}

// command answers one message from the allowed chat.
func (b *Bot) command(ctx context.Context, text string) (string, [][]button) {
	text = strings.TrimSpace(text)
	if !strings.HasPrefix(text, "/") {
		return "Не понимаю. " + helpText, nil
	}
	cmd, arg, _ := strings.Cut(text, " ")
	cmd, _, _ = strings.Cut(cmd, "@") // /status@srvmon_bot in groups
	arg = strings.TrimSpace(arg)
	switch strings.ToLower(cmd) {
	case "/start", "/help":
		return helpText, nil
	case "/status":
		return b.statusText(ctx), nil
	case "/server":
		if arg == "" {
			return "Укажите имя: /server &lt;имя&gt;", nil
		}
		return b.serverText(ctx, arg), nil
	case "/alerts":
		return b.alertsText(ctx), nil
	case "/ack":
		id, err := strconv.ParseInt(strings.TrimPrefix(arg, "#"), 10, 64)
		if err != nil {
			return "Укажите номер: /ack 42", nil
		}
		return esc(b.ack(ctx, id)), nil
	case "/bills":
		return b.billsText(ctx), nil
	case "/ask":
		if arg == "" {
			return "Задайте вопрос: /ask что с диском на shop-db?", nil
		}
		b.mu.Lock()
		ask := b.ask
		b.mu.Unlock()
		if ask == nil {
			return "AI-агент не настроен: нужен ключ SRVMON_AI_API_KEY (см. README).", nil
		}
		// The agent may take minutes and may wait for a confirmation that
		// comes as a button press of this same loop: answer from the side.
		go func() {
			actx, cancel := context.WithTimeout(context.WithoutCancel(ctx), askTimeout)
			defer cancel()
			ans, err := ask(actx, arg)
			if err != nil {
				b.Send("Агент не ответил: " + esc(err.Error()))
				return
			}
			b.Send("🤖 " + esc(ans))
		}()
		return "🤖 агент думает…", nil
	}
	return "Неизвестная команда. " + helpText, nil
}

func pct(used, total uint64) float64 {
	if total == 0 {
		return 0
	}
	return float64(used) / float64(total) * 100
}

func (b *Bot) statusText(ctx context.Context) string {
	servers, err := b.st.ListServers(ctx)
	if err != nil {
		return "ошибка базы: " + esc(err.Error())
	}
	open, _ := b.st.OpenAlerts(ctx)
	now := b.now()
	online, crit := 0, 0
	for _, a := range open {
		if a.Severity == store.SevCrit {
			crit++
		}
	}
	var lines []string
	for _, s := range servers {
		snap, ok := b.latest.Get(s.ID)
		if !ok || now.Sub(snap.Received) >= offlineAfter {
			since := "нет данных"
			if s.LastSeen != nil {
				since = "молчит " + alerts.FormatDuration(now.Sub(*s.LastSeen))
			}
			lines = append(lines, fmt.Sprintf("○ <b>%s</b> — %s", esc(s.Name), since))
			continue
		}
		online++
		h := snap.Report.Host
		disk := 0.0
		for _, d := range h.Disks {
			disk = max(disk, pct(d.Used, d.Total))
		}
		line := fmt.Sprintf("● <b>%s</b> cpu %.0f%% · ram %.0f%% · disk %.0f%%", esc(s.Name), h.CPUPercent,
			pct(h.MemUsed(), h.MemTotal), disk)
		if snap.Report.DockerOK {
			run, bad := 0, 0
			for _, c := range snap.Report.Containers {
				if c.State == "running" {
					run++
				}
				if c.State != "running" || c.Health == "unhealthy" {
					bad++
				}
			}
			line += fmt.Sprintf(" · docker %d/%d", run, len(snap.Report.Containers))
			if bad > 0 {
				line += fmt.Sprintf(" ⚠%d", bad)
			}
		}
		lines = append(lines, line)
	}
	head := fmt.Sprintf("<b>srvmon</b>: %d/%d онлайн · алертов %d", online, len(servers), len(open))
	if crit > 0 {
		head += fmt.Sprintf(" (crit %d)", crit)
	}
	if len(servers) == 0 {
		return head + "\nСерверов пока нет."
	}
	return head + "\n\n" + strings.Join(lines, "\n")
}

func (b *Bot) findServer(ctx context.Context, name string) (store.Server, string) {
	servers, err := b.st.ListServers(ctx)
	if err != nil {
		return store.Server{}, "ошибка базы"
	}
	var prefix []store.Server
	for _, s := range servers {
		if strings.EqualFold(s.Name, name) {
			return s, ""
		}
		if strings.HasPrefix(strings.ToLower(s.Name), strings.ToLower(name)) {
			prefix = append(prefix, s)
		}
	}
	switch len(prefix) {
	case 1:
		return prefix[0], ""
	case 0:
		return store.Server{}, "сервер «" + esc(name) + "» не найден"
	}
	names := make([]string, len(prefix))
	for i, s := range prefix {
		names[i] = esc(s.Name)
	}
	return store.Server{}, "уточните: " + strings.Join(names, ", ")
}

func (b *Bot) serverText(ctx context.Context, name string) string {
	s, errText := b.findServer(ctx, name)
	if errText != "" {
		return errText
	}
	var out strings.Builder
	fmt.Fprintf(&out, "<b>%s</b>", esc(s.Name))
	if s.Description != "" {
		fmt.Fprintf(&out, " — %s", esc(s.Description))
	}
	out.WriteString("\n")
	snap, ok := b.latest.Get(s.ID)
	now := b.now()
	if !ok || now.Sub(snap.Received) >= offlineAfter {
		if s.LastSeen != nil {
			fmt.Fprintf(&out, "○ агент молчит %s\n", alerts.FormatDuration(now.Sub(*s.LastSeen)))
		} else {
			out.WriteString("○ нет данных от агента\n")
		}
	} else {
		h := snap.Report.Host
		fmt.Fprintf(&out, "● онлайн · uptime %s\n", alerts.FormatDuration(time.Duration(h.UptimeSec)*time.Second))
		fmt.Fprintf(&out, "cpu %.0f%% (%d ядер) · load %.2f %.2f %.2f\n", h.CPUPercent, h.CPUCores, h.Load1, h.Load5, h.Load15)
		fmt.Fprintf(&out, "ram %.0f%% из %s", pct(h.MemUsed(), h.MemTotal), bytesText(h.MemTotal))
		if h.SwapTotal > 0 {
			fmt.Fprintf(&out, " · swap %.0f%%", pct(h.SwapTotal-h.SwapFree, h.SwapTotal))
		}
		out.WriteString("\n")
		for _, d := range h.Disks {
			fmt.Fprintf(&out, "disk %s %.0f%% из %s\n", esc(d.Mount), pct(d.Used, d.Total), bytesText(d.Total))
		}
		if snap.Report.DockerOK {
			cs := snap.Report.Containers
			fmt.Fprintf(&out, "\n<b>контейнеры</b> (%d):\n", len(cs))
			sorted := append(cs[:0:0], cs...)
			sort.SliceStable(sorted, func(i, j int) bool {
				return (sorted[i].State == "running") && !(sorted[j].State == "running")
			})
			for i, c := range sorted {
				if i >= 30 {
					fmt.Fprintf(&out, "… ещё %d\n", len(sorted)-i)
					break
				}
				mark := "●"
				if c.State != "running" || c.Health == "unhealthy" {
					mark = "⚠"
				}
				fmt.Fprintf(&out, "%s %s — %s\n", mark, esc(c.Name), esc(c.Status))
			}
		}
	}
	open, _ := b.st.OpenAlerts(ctx)
	first := true
	for _, a := range open {
		if a.ServerID != s.ID {
			continue
		}
		if first {
			out.WriteString("\n<b>алерты</b>:\n")
			first = false
		}
		fmt.Fprintf(&out, "#%d %s %s\n", a.ID, sevIcon(a.Severity), esc(a.Message))
	}
	return strings.TrimRight(out.String(), "\n")
}

func (b *Bot) alertsText(ctx context.Context) string {
	open, err := b.st.OpenAlerts(ctx)
	if err != nil {
		return "ошибка базы"
	}
	if len(open) == 0 {
		return "✅ Активных алертов нет."
	}
	loc := b.loc(ctx)
	var out strings.Builder
	fmt.Fprintf(&out, "<b>Активные алерты</b> (%d):\n", len(open))
	for _, a := range open {
		state := ""
		if a.State == store.AlertAcked {
			state = " ✓"
		}
		fmt.Fprintf(&out, "\n#%d %s · <b>%s</b>%s\n%s · %s", a.ID, sevIcon(a.Severity), esc(a.ServerName), state,
			esc(a.Message), a.StartedAt.In(loc).Format("02.01 15:04"))
	}
	out.WriteString("\n\nПодтвердить: /ack &lt;id&gt;")
	return out.String()
}

// monthly converts a cost to a monthly amount by the billing period.
func monthly(cost float64, period string) float64 {
	switch period {
	case "year":
		return cost / 12
	case "hour":
		return cost * 730
	}
	return cost
}

func (b *Bot) billsText(ctx context.Context) string {
	servers, err := b.st.ListServers(ctx)
	if err != nil {
		return "ошибка базы"
	}
	now := b.now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	type bill struct {
		s    store.Server
		days int
		due  time.Time
	}
	var bills []bill
	totals := map[string]float64{}
	for _, s := range servers {
		if s.Cost != nil && s.Currency != "" {
			totals[s.Currency] += monthly(*s.Cost, s.Period)
		}
		due, err := time.Parse("2006-01-02", s.NextPayment)
		if err != nil {
			continue
		}
		bills = append(bills, bill{s: s, due: due, days: int(due.Sub(today).Hours() / 24)})
	}
	sort.Slice(bills, func(i, j int) bool { return bills[i].due.Before(bills[j].due) })
	var out strings.Builder
	out.WriteString("<b>Оплаты серверов</b>\n")
	if len(bills) == 0 {
		out.WriteString("Даты оплат не заполнены: укажите их в паспорте сервера.\n")
	}
	for _, bl := range bills {
		mark := "·"
		switch {
		case bl.days < 0:
			mark = "🔴"
		case bl.days <= 3:
			mark = "🟡"
		}
		fmt.Fprintf(&out, "%s <b>%s</b>: %s\n", mark, esc(bl.s.Name), esc(alerts.PaymentText(bl.s, bl.days)))
	}
	if len(totals) > 0 {
		cur := make([]string, 0, len(totals))
		for c := range totals {
			cur = append(cur, c)
		}
		sort.Strings(cur)
		parts := make([]string, len(cur))
		for i, c := range cur {
			parts[i] = fmt.Sprintf("%.2f %s", totals[c], esc(c))
		}
		fmt.Fprintf(&out, "\nВ месяц: %s", strings.Join(parts, " + "))
	}
	return strings.TrimRight(out.String(), "\n")
}

func bytesText(n uint64) string {
	const gib = 1 << 30
	if n >= gib {
		return fmt.Sprintf("%.1f GiB", float64(n)/gib)
	}
	return fmt.Sprintf("%d MiB", n>>20)
}
