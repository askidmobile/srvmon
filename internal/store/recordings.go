package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Terminal recordings: one row per web terminal session, the encrypted
// asciicast itself lives in <data>/recordings/<file_id>.rec. Rows outlive
// their server card (server_name is kept) and go away by retention.
const migrationRecordings = `CREATE TABLE recordings (
		id          INTEGER PRIMARY KEY AUTOINCREMENT,
		file_id     TEXT NOT NULL UNIQUE,
		server_id   INTEGER NOT NULL,
		server_name TEXT NOT NULL,
		target      TEXT NOT NULL,  -- user@server or server:container
		ip          TEXT NOT NULL DEFAULT '',
		cols        INTEGER NOT NULL,
		rows        INTEGER NOT NULL,
		started_at  INTEGER NOT NULL, -- unix ms
		ended_at    INTEGER,          -- unix ms, NULL while the session runs
		bytes       INTEGER NOT NULL DEFAULT 0,
		truncated   INTEGER NOT NULL DEFAULT 0
	);
	CREATE INDEX recordings_started ON recordings(started_at);`

// Settings of terminal recording.
const (
	KeyRecEnabled   = "rec.enabled"   // "1" | "0", default on
	KeyRecRetention = "rec.retention" // days
	KeyRecMaxMB     = "rec.max_mb"    // per recording
)

// Recording is a recorded terminal session.
type Recording struct {
	ID         int64      `json:"id"`
	FileID     string     `json:"-"`
	ServerID   int64      `json:"server_id"`
	ServerName string     `json:"server_name"`
	Target     string     `json:"target"`
	IP         string     `json:"ip"`
	Cols       int        `json:"cols"`
	Rows       int        `json:"rows"`
	StartedAt  time.Time  `json:"started_at"`
	EndedAt    *time.Time `json:"ended_at,omitempty"`
	Bytes      int64      `json:"bytes"`
	Truncated  bool       `json:"truncated"`
}

// CreateRecording inserts a running recording and returns its id.
func (s *Store) CreateRecording(ctx context.Context, r Recording) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO recordings
		(file_id, server_id, server_name, target, ip, cols, rows, started_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.FileID, r.ServerID, r.ServerName, r.Target, r.IP, r.Cols, r.Rows, r.StartedAt.UnixMilli())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishRecording stores the end of a recording.
func (s *Store) FinishRecording(ctx context.Context, id int64, ended time.Time, bytes int64, truncated bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recordings SET ended_at = ?, bytes = ?, truncated = ? WHERE id = ?`,
		ended.UnixMilli(), bytes, truncated, id)
	return err
}

// CloseStaleRecordings ends recordings left running by a restart: their
// sessions are gone, the chunks written so far still play.
func (s *Store) CloseStaleRecordings(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE recordings SET ended_at = ? WHERE ended_at IS NULL`, now.UnixMilli())
	return err
}

const recordingCols = `id, file_id, server_id, server_name, target, ip, cols, rows, started_at, ended_at, bytes, truncated`

func scanRecording(row scanner) (Recording, error) {
	var r Recording
	var started int64
	var ended sql.NullInt64
	if err := row.Scan(&r.ID, &r.FileID, &r.ServerID, &r.ServerName, &r.Target, &r.IP, &r.Cols, &r.Rows,
		&started, &ended, &r.Bytes, &r.Truncated); err != nil {
		return r, err
	}
	r.StartedAt = time.UnixMilli(started).UTC()
	if ended.Valid {
		t := time.UnixMilli(ended.Int64).UTC()
		r.EndedAt = &t
	}
	return r, nil
}

// ListRecordings returns recordings newest first; serverID 0 means all,
// before > 0 continues a page (ids below it).
func (s *Store) ListRecordings(ctx context.Context, serverID, before int64, limit int) ([]Recording, error) {
	q := `SELECT ` + recordingCols + ` FROM recordings WHERE 1 = 1`
	args := []any{}
	if serverID > 0 {
		q += ` AND server_id = ?`
		args = append(args, serverID)
	}
	if before > 0 {
		q += ` AND id < ?`
		args = append(args, before)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Recording{}
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordingServer is a server that has recordings, for the list filter.
type RecordingServer struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// RecordingServers lists servers with recordings, by name.
func (s *Store) RecordingServers(ctx context.Context) ([]RecordingServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT server_id, MAX(server_name), COUNT(*) FROM recordings
		GROUP BY server_id ORDER BY 2`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []RecordingServer{}
	for rows.Next() {
		var r RecordingServer
		if err := rows.Scan(&r.ID, &r.Name, &r.Count); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRecording returns one recording.
func (s *Store) GetRecording(ctx context.Context, id int64) (Recording, error) {
	r, err := scanRecording(s.db.QueryRowContext(ctx, `SELECT `+recordingCols+` FROM recordings WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	return r, err
}

// DeleteRecording removes a row.
func (s *Store) DeleteRecording(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM recordings WHERE id = ?`, id)
	return err
}

// FinishedRecordingsBefore returns ended recordings that started before t
// (oldest first), for the retention purge. Running ones are never returned.
func (s *Store) FinishedRecordingsBefore(ctx context.Context, t time.Time) ([]Recording, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+recordingCols+` FROM recordings
		WHERE started_at < ? AND ended_at IS NOT NULL ORDER BY id`, t.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recording
	for rows.Next() {
		r, err := scanRecording(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RecordingFileIDs returns every file id in the table, to find orphan files.
func (s *Store) RecordingFileIDs(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT file_id FROM recordings`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}
