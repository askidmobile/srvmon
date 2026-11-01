package store

import (
	"context"
	"time"
)

// Check kinds of the panel-side availability checks.
const (
	KindCheckDown    = "check_down"    // an HTTP/TCP check fails
	KindCertExpiry   = "cert_expiry"   // a TLS certificate expires in ≤ threshold days
	KindDomainExpiry = "domain_expiry" // a domain registration expires in ≤ threshold days
)

// MaxChecksPerServer caps the targets of one server.
const MaxChecksPerServer = 20

const migrationChecks = `CREATE TABLE checks (
		-- AUTOINCREMENT: a removed and re-added target gets a new id, so
		-- the result of the old one never shows up under the new one.
		id         INTEGER PRIMARY KEY AUTOINCREMENT,
		server_id  INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
		target     TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		UNIQUE (server_id, target)
	);
	INSERT INTO alert_rules (kind, enabled, threshold, duration_sec, severity, channel) VALUES
		('check_down', 1, 0, 120, 'crit', 'tg'),
		('cert_expiry', 1, 14, 0, 'warn', 'tg'),
		('domain_expiry', 1, 30, 0, 'warn', 'tg');`

// Check is one target checked from the panel: an http(s):// URL,
// tls://host:port or host:port (TCP).
type Check struct {
	ID       int64     `json:"id"`
	ServerID int64     `json:"server_id"`
	Target   string    `json:"target"`
	Created  time.Time `json:"created_at"`
}

// ListChecks returns all checks of all servers.
func (s *Store) ListChecks(ctx context.Context) ([]Check, error) {
	return s.queryChecks(ctx, `SELECT id, server_id, target, created_at FROM checks ORDER BY server_id, id`)
}

// ServerChecks returns the checks of one server in the order they were added.
func (s *Store) ServerChecks(ctx context.Context, serverID int64) ([]Check, error) {
	return s.queryChecks(ctx, `SELECT id, server_id, target, created_at FROM checks WHERE server_id = ? ORDER BY id`, serverID)
}

func (s *Store) queryChecks(ctx context.Context, q string, args ...any) ([]Check, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Check{}
	for rows.Next() {
		var c Check
		var created int64
		if err := rows.Scan(&c.ID, &c.ServerID, &c.Target, &created); err != nil {
			return nil, err
		}
		c.Created = time.Unix(created, 0).UTC()
		out = append(out, c)
	}
	return out, rows.Err()
}

// SetServerChecks replaces the targets of a server. Targets that stay keep
// their id (and so their last result); the order of targets is kept for new
// ones. Returns ErrNotFound when the server does not exist.
func (s *Store) SetServerChecks(ctx context.Context, serverID int64, targets []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM servers WHERE id = ?`, serverID).Scan(&n); err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	keep := map[string]bool{}
	for _, t := range targets {
		keep[t] = true
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, target FROM checks WHERE server_id = ?`, serverID)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	var drop []int64
	for rows.Next() {
		var id int64
		var t string
		if err := rows.Scan(&id, &t); err != nil {
			rows.Close()
			return err
		}
		have[t] = true
		if !keep[t] {
			drop = append(drop, id)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range drop {
		if _, err := tx.ExecContext(ctx, `DELETE FROM checks WHERE id = ?`, id); err != nil {
			return err
		}
	}
	now := time.Now().Unix()
	for _, t := range targets {
		if have[t] {
			continue
		}
		have[t] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO checks (server_id, target, created_at) VALUES (?, ?, ?)`,
			serverID, t, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}
