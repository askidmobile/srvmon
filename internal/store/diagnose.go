package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/i18n"
)

// AI diagnoses of alerts: when an alert fires, the agent may collect logs
// read-only and explain the likely cause. One diagnosis per alert (a manual
// rerun replaces it); rows go away with their alert.
const migrationDiagnoses = `CREATE TABLE alert_diagnoses (
		alert_id    INTEGER PRIMARY KEY REFERENCES alerts(id) ON DELETE CASCADE,
		state       TEXT NOT NULL,              -- queued | running | done | error | skipped
		trigger     TEXT NOT NULL,              -- auto | manual
		text        TEXT NOT NULL DEFAULT '',
		tools       INTEGER NOT NULL DEFAULT 0, -- commands run
		started_at  INTEGER NOT NULL,
		finished_at INTEGER
	);
	CREATE INDEX alert_diagnoses_started ON alert_diagnoses(started_at);
	-- Every run started, kept apart from the shown result: a rerun replaces
	-- the diagnosis of an alert but must not erase its cost from the limits.
	CREATE TABLE diagnosis_runs (
		id         INTEGER PRIMARY KEY,
		alert_id   INTEGER NOT NULL,
		rule_id    INTEGER NOT NULL,
		server_id  INTEGER NOT NULL,
		subject    TEXT NOT NULL,
		trigger    TEXT NOT NULL,
		started_at INTEGER NOT NULL
	);
	CREATE INDEX diagnosis_runs_started ON diagnosis_runs(started_at);`

// Diagnosis states.
const (
	DiagQueued  = "queued"
	DiagRunning = "running"
	DiagDone    = "done"
	DiagError   = "error"
	DiagSkipped = "skipped"
)

// Diagnosis is the AI agent's read-only investigation of an alert.
type Diagnosis struct {
	AlertID    int64      `json:"alert_id"`
	State      string     `json:"state"`
	Trigger    string     `json:"trigger"`
	Text       string     `json:"text"`
	Tools      int        `json:"tools"`
	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at,omitempty"`
}

const diagCols = `alert_id, state, trigger, text, tools, started_at, finished_at`

func scanDiagnosis(row scanner) (Diagnosis, error) {
	var d Diagnosis
	var started int64
	var finished sql.NullInt64
	if err := row.Scan(&d.AlertID, &d.State, &d.Trigger, &d.Text, &d.Tools, &started, &finished); err != nil {
		return d, err
	}
	d.StartedAt = time.Unix(started, 0).UTC()
	if finished.Valid {
		t := time.Unix(finished.Int64, 0).UTC()
		d.FinishedAt = &t
	}
	return d, nil
}

// PutDiagnosis creates (or replaces) the diagnosis of an alert.
func (s *Store) PutDiagnosis(ctx context.Context, d Diagnosis) error {
	var finished any
	if d.FinishedAt != nil {
		finished = d.FinishedAt.Unix()
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO alert_diagnoses (`+diagCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(alert_id) DO UPDATE SET state = excluded.state, trigger = excluded.trigger, text = excluded.text,
		tools = excluded.tools, started_at = excluded.started_at, finished_at = excluded.finished_at`,
		d.AlertID, d.State, d.Trigger, d.Text, d.Tools, d.StartedAt.Unix(), finished)
	return err
}

// SetDiagnosisState moves a diagnosis to running.
func (s *Store) SetDiagnosisState(ctx context.Context, alertID int64, state string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_diagnoses SET state = ? WHERE alert_id = ?`, state, alertID)
	return err
}

// FinishDiagnosis stores the outcome of a diagnosis.
func (s *Store) FinishDiagnosis(ctx context.Context, alertID int64, state, text string, tools int, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_diagnoses SET state = ?, text = ?, tools = ?, finished_at = ?
		WHERE alert_id = ?`, state, text, tools, at.Unix(), alertID)
	return err
}

// GetDiagnosis returns the diagnosis of an alert or ErrNotFound.
func (s *Store) GetDiagnosis(ctx context.Context, alertID int64) (Diagnosis, error) {
	d, err := scanDiagnosis(s.db.QueryRowContext(ctx, `SELECT `+diagCols+` FROM alert_diagnoses WHERE alert_id = ?`, alertID))
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrNotFound
	}
	return d, err
}

// AttachDiagnoses fills Alert.Diagnosis for the alerts that have one.
func (s *Store) AttachDiagnoses(ctx context.Context, lists ...[]Alert) error {
	var ids []any
	for _, l := range lists {
		for _, a := range l {
			ids = append(ids, a.ID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	q := `SELECT ` + diagCols + ` FROM alert_diagnoses WHERE alert_id IN (?` + strings.Repeat(", ?", len(ids)-1) + `)`
	rows, err := s.db.QueryContext(ctx, q, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	by := map[int64]*Diagnosis{}
	for rows.Next() {
		d, err := scanDiagnosis(rows)
		if err != nil {
			return err
		}
		by[d.AlertID] = &d
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, l := range lists {
		for i := range l {
			l[i].Diagnosis = by[l[i].ID]
		}
	}
	return nil
}

// RecordDiagnosisRun counts a started diagnosis run towards the limits.
func (s *Store) RecordDiagnosisRun(ctx context.Context, a Alert, trigger string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO diagnosis_runs (alert_id, rule_id, server_id, subject, trigger, started_at)
		VALUES (?, ?, ?, ?, ?, ?)`, a.ID, a.RuleID, a.ServerID, a.Subject, trigger, at.Unix())
	return err
}

// CountDiagnosesSince counts diagnosis runs started at or after t.
func (s *Store) CountDiagnosesSince(ctx context.Context, t time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnosis_runs WHERE started_at >= ?`, t.Unix()).Scan(&n)
	return n, err
}

// LastAutoDiagnosis returns when the same rule, server and subject was last
// diagnosed automatically (zero if never).
func (s *Store) LastAutoDiagnosis(ctx context.Context, k SilenceKey) (time.Time, error) {
	var at sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT MAX(started_at) FROM diagnosis_runs
		WHERE rule_id = ? AND server_id = ? AND subject = ? AND trigger = 'auto'`,
		k.RuleID, k.ServerID, k.Subject).Scan(&at)
	if err != nil || !at.Valid {
		return time.Time{}, err
	}
	return time.Unix(at.Int64, 0).UTC(), nil
}

// ResetRunningDiagnoses marks diagnoses cut short by a restart as failed.
func (s *Store) ResetRunningDiagnoses(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `UPDATE alert_diagnoses SET state = 'error', text = ?, finished_at = ?
		WHERE state IN ('queued', 'running')`, i18n.T("interrupted by a srvmon restart"), time.Now().Unix())
	return err
}
