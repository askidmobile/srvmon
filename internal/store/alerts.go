package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Rule kinds. State kinds are evaluated by the alert engine on every tick;
// event kinds fire once per event (fail2ban ban, login, ...).
const (
	KindAgentSilent       = "agent_silent"
	KindCPU               = "cpu"
	KindMem               = "mem"
	KindDisk              = "disk"
	KindContainerDown     = "container_down"
	KindContainerRestarts = "container_restarts"
	KindPaymentDue        = "payment_due"
	KindFail2ban          = "fail2ban"
	KindBruteforce        = "bruteforce"
	KindLogin             = "login"
)

// EventKind reports whether a rule kind fires on events, not on state.
func EventKind(kind string) bool {
	return kind == KindFail2ban || kind == KindBruteforce || kind == KindLogin
}

// KnownKind reports whether kind is a supported rule kind.
func KnownKind(kind string) bool {
	switch kind {
	case KindAgentSilent, KindCPU, KindMem, KindDisk, KindContainerDown, KindContainerRestarts,
		KindPaymentDue, KindFail2ban, KindBruteforce, KindLogin:
		return true
	}
	return false
}

// Alert states.
const (
	AlertFiring   = "firing"
	AlertAcked    = "acked"
	AlertResolved = "resolved"
	AlertEvent    = "event" // one-off event, nothing to resolve
)

// Severities and channels.
const (
	SevCrit = "crit"
	SevWarn = "warn"
	SevInfo = "info"

	ChanTelegram = "tg"
	ChanWeb      = "web"
)

const migrationAlerts = `CREATE TABLE alert_rules (
		id           INTEGER PRIMARY KEY,
		kind         TEXT NOT NULL,
		enabled      INTEGER NOT NULL DEFAULT 1,
		threshold    REAL NOT NULL DEFAULT 0,
		duration_sec INTEGER NOT NULL DEFAULT 0,
		severity     TEXT NOT NULL,
		channel      TEXT NOT NULL
	);
	CREATE TABLE alerts (
		id          INTEGER PRIMARY KEY,
		rule_id     INTEGER NOT NULL,
		kind        TEXT NOT NULL,
		server_id   INTEGER NOT NULL DEFAULT 0, -- 0: the panel itself
		server_name TEXT NOT NULL DEFAULT '',
		subject     TEXT NOT NULL DEFAULT '',   -- container, mount, ip, ...
		severity    TEXT NOT NULL,
		channel     TEXT NOT NULL,
		state       TEXT NOT NULL,
		message     TEXT NOT NULL,
		started_at  INTEGER NOT NULL,
		acked_at    INTEGER,
		resolved_at INTEGER,
		notified    INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX alerts_state ON alerts (state);
	CREATE INDEX alerts_started ON alerts (started_at);
	CREATE TABLE alert_silences (
		rule_id   INTEGER NOT NULL,
		server_id INTEGER NOT NULL,
		subject   TEXT NOT NULL,
		until     INTEGER NOT NULL,
		PRIMARY KEY (rule_id, server_id, subject)
	);
	INSERT INTO alert_rules (kind, enabled, threshold, duration_sec, severity, channel) VALUES
		('agent_silent', 1, 0, 120, 'crit', 'tg'),
		('cpu', 1, 90, 300, 'warn', 'tg'),
		('disk', 1, 85, 0, 'warn', 'tg'),
		('container_down', 1, 0, 60, 'crit', 'tg'),
		('container_restarts', 1, 2, 3600, 'warn', 'tg'),
		('payment_due', 1, 3, 0, 'warn', 'tg'),
		('mem', 0, 95, 300, 'warn', 'web'),
		('fail2ban', 1, 0, 0, 'warn', 'tg'),
		('bruteforce', 1, 0, 0, 'warn', 'tg'),
		('login', 1, 0, 0, 'info', 'tg');`

// Rule is one alert rule. Threshold is percent for cpu/mem/disk, a restart
// count for container_restarts and days for payment_due. Duration is how long
// the condition must hold (for agent_silent: how long the agent is quiet, for
// container_restarts: the counting window).
type Rule struct {
	ID          int64   `json:"id"`
	Kind        string  `json:"kind"`
	Enabled     bool    `json:"enabled"`
	Threshold   float64 `json:"threshold"`
	DurationSec int64   `json:"duration_sec"`
	Severity    string  `json:"severity"`
	Channel     string  `json:"channel"`
}

// Duration returns the rule duration.
func (r Rule) Duration() time.Duration { return time.Duration(r.DurationSec) * time.Second }

const ruleCols = `id, kind, enabled, threshold, duration_sec, severity, channel`

func scanRule(row scanner) (Rule, error) {
	var r Rule
	err := row.Scan(&r.ID, &r.Kind, &r.Enabled, &r.Threshold, &r.DurationSec, &r.Severity, &r.Channel)
	return r, err
}

// ListRules returns all alert rules in creation order.
func (s *Store) ListRules(ctx context.Context) ([]Rule, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+ruleCols+` FROM alert_rules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Rule{}
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRule returns one rule or ErrNotFound.
func (s *Store) GetRule(ctx context.Context, id int64) (Rule, error) {
	r, err := scanRule(s.db.QueryRowContext(ctx, `SELECT `+ruleCols+` FROM alert_rules WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// CreateRule inserts a rule and returns its id.
func (s *Store) CreateRule(ctx context.Context, r Rule) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO alert_rules (kind, enabled, threshold, duration_sec, severity, channel)
		VALUES (?, ?, ?, ?, ?, ?)`, r.Kind, r.Enabled, r.Threshold, r.DurationSec, r.Severity, r.Channel)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateRule replaces the editable fields of a rule (the kind is fixed).
func (s *Store) UpdateRule(ctx context.Context, r Rule) error {
	res, err := s.db.ExecContext(ctx, `UPDATE alert_rules SET enabled = ?, threshold = ?, duration_sec = ?,
		severity = ?, channel = ? WHERE id = ?`, r.Enabled, r.Threshold, r.DurationSec, r.Severity, r.Channel, r.ID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteRule removes a rule and its silences. Its open alerts are resolved
// by the engine on the next tick.
func (s *Store) DeleteRule(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM alert_rules WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM alert_silences WHERE rule_id = ?`, id)
	return err
}

// Alert is one firing (or past) alert or event.
type Alert struct {
	ID         int64      `json:"id"`
	RuleID     int64      `json:"rule_id"`
	Kind       string     `json:"kind"`
	ServerID   int64      `json:"server_id"`
	ServerName string     `json:"server_name"`
	Subject    string     `json:"subject"`
	Severity   string     `json:"severity"`
	Channel    string     `json:"channel"`
	State      string     `json:"state"`
	Message    string     `json:"message"`
	StartedAt  time.Time  `json:"started_at"`
	AckedAt    *time.Time `json:"acked_at,omitempty"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
	Notified   bool       `json:"notified"`
}

// Open reports whether the alert is firing or acknowledged.
func (a Alert) Open() bool { return a.State == AlertFiring || a.State == AlertAcked }

const alertCols = `id, rule_id, kind, server_id, server_name, subject, severity, channel, state, message,
	started_at, acked_at, resolved_at, notified`

func scanAlert(row scanner) (Alert, error) {
	var a Alert
	var started int64
	var acked, resolved sql.NullInt64
	if err := row.Scan(&a.ID, &a.RuleID, &a.Kind, &a.ServerID, &a.ServerName, &a.Subject, &a.Severity, &a.Channel,
		&a.State, &a.Message, &started, &acked, &resolved, &a.Notified); err != nil {
		return a, err
	}
	a.StartedAt = time.Unix(started, 0).UTC()
	if acked.Valid {
		t := time.Unix(acked.Int64, 0).UTC()
		a.AckedAt = &t
	}
	if resolved.Valid {
		t := time.Unix(resolved.Int64, 0).UTC()
		a.ResolvedAt = &t
	}
	return a, nil
}

func (s *Store) queryAlerts(ctx context.Context, q string, args ...any) ([]Alert, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+alertCols+` FROM alerts `+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Alert{}
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OpenAlerts returns firing and acknowledged alerts, newest first.
func (s *Store) OpenAlerts(ctx context.Context) ([]Alert, error) {
	return s.queryAlerts(ctx, `WHERE state IN ('firing', 'acked') ORDER BY started_at DESC, id DESC`)
}

// RecentAlerts returns the newest alerts of any state.
func (s *Store) RecentAlerts(ctx context.Context, limit int) ([]Alert, error) {
	return s.queryAlerts(ctx, `ORDER BY started_at DESC, id DESC LIMIT ?`, limit)
}

// GetAlert returns one alert or ErrNotFound.
func (s *Store) GetAlert(ctx context.Context, id int64) (Alert, error) {
	list, err := s.queryAlerts(ctx, `WHERE id = ?`, id)
	if err != nil {
		return Alert{}, err
	}
	if len(list) == 0 {
		return Alert{}, ErrNotFound
	}
	return list[0], nil
}

// CountAlertsSince counts alerts and events started at or after t.
func (s *Store) CountAlertsSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM alerts WHERE started_at >= ?`, t.Unix()).Scan(&n)
	return n, err
}

// CreateAlert inserts an alert and returns it with its id.
func (s *Store) CreateAlert(ctx context.Context, a Alert) (Alert, error) {
	var resolved any
	if a.ResolvedAt != nil {
		resolved = a.ResolvedAt.Unix()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO alerts (rule_id, kind, server_id, server_name, subject, severity,
		channel, state, message, started_at, resolved_at, notified) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.RuleID, a.Kind, a.ServerID, a.ServerName, a.Subject, a.Severity, a.Channel, a.State, a.Message,
		a.StartedAt.Unix(), resolved, a.Notified)
	if err != nil {
		return a, err
	}
	a.ID, err = res.LastInsertId()
	return a, err
}

// ResolveAlert closes an open alert. It returns false when the alert was
// already closed.
func (s *Store) ResolveAlert(ctx context.Context, id int64, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE alerts SET state = 'resolved', resolved_at = ?
		WHERE id = ? AND state IN ('firing', 'acked')`, at.Unix(), id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// AckAlert acknowledges a firing alert. It returns ErrNotFound when the alert
// does not exist and false when it is not firing.
func (s *Store) AckAlert(ctx context.Context, id int64, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE alerts SET state = 'acked', acked_at = ? WHERE id = ? AND state = 'firing'`,
		at.Unix(), id)
	if err != nil {
		return false, err
	}
	if n, _ := res.RowsAffected(); n == 1 {
		return true, nil
	}
	if _, err := s.GetAlert(ctx, id); err != nil {
		return false, err
	}
	return false, nil
}

// SetAlertMessage updates the text of an open alert (e.g. a new value).
func (s *Store) SetAlertMessage(ctx context.Context, id int64, msg string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET message = ? WHERE id = ?`, msg, id)
	return err
}

// PurgeAlerts deletes closed alerts and events older than t, and expired
// silences.
func (s *Store) PurgeAlerts(ctx context.Context, t time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM alerts WHERE state IN ('resolved', 'event') AND started_at < ?`,
		t.Unix()); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `DELETE FROM alert_silences WHERE until < ?`, time.Now().Unix())
	return err
}

// SilenceKey identifies what a silence mutes: one rule on one server and
// subject.
type SilenceKey struct {
	RuleID   int64
	ServerID int64
	Subject  string
}

// SetSilence mutes notifications for key until the given time.
func (s *Store) SetSilence(ctx context.Context, k SilenceKey, until time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_silences (rule_id, server_id, subject, until) VALUES (?, ?, ?, ?)
		ON CONFLICT(rule_id, server_id, subject) DO UPDATE SET until = excluded.until`,
		k.RuleID, k.ServerID, k.Subject, until.Unix())
	return err
}

// SilencedUntil returns the end of an active silence for key (zero if none).
func (s *Store) SilencedUntil(ctx context.Context, k SilenceKey, now time.Time) (time.Time, error) {
	var until int64
	err := s.db.QueryRowContext(ctx, `SELECT until FROM alert_silences WHERE rule_id = ? AND server_id = ? AND subject = ?
		AND until > ?`, k.RuleID, k.ServerID, k.Subject, now.Unix()).Scan(&until)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, nil
	}
	if err != nil {
		return time.Time{}, err
	}
	return time.Unix(until, 0).UTC(), nil
}

// MarkNotified records that a notification was sent for the alert.
func (s *Store) MarkNotified(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alerts SET notified = 1 WHERE id = ?`, id)
	return err
}
