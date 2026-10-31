// Package store keeps srvmon configuration and state in SQLite.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, keeps the binary CGO-free
)

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("store: not found")

// Store wraps the SQLite database.
type Store struct {
	db *sql.DB
}

// Open opens (and migrates) the database at path. Use ":memory:" in tests.
func Open(path string) (*Store, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite: one writer; keeps :memory: shared too
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE sessions (
		id_hash    TEXT PRIMARY KEY,
		kind       TEXT NOT NULL,      -- pending (password ok) | full
		ip         TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		expires_at INTEGER NOT NULL
	);
	CREATE TABLE recovery_codes (code_hash TEXT PRIMARY KEY, used_at INTEGER);
	CREATE TABLE servers (
		id          INTEGER PRIMARY KEY,
		name        TEXT NOT NULL UNIQUE,
		description TEXT NOT NULL DEFAULT '',
		project     TEXT NOT NULL DEFAULT '',
		grp         TEXT NOT NULL DEFAULT '',
		tags        TEXT NOT NULL DEFAULT '[]',
		host        TEXT NOT NULL DEFAULT '',
		port        INTEGER NOT NULL DEFAULT 22,
		provider    TEXT NOT NULL DEFAULT '',
		plan        TEXT NOT NULL DEFAULT '',
		cost        REAL,
		currency    TEXT NOT NULL DEFAULT '',
		next_payment TEXT NOT NULL DEFAULT '',
		created_at  INTEGER NOT NULL
	);
	CREATE TABLE agent_tokens (
		id           INTEGER PRIMARY KEY,
		server_id    INTEGER NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
		token_hash   TEXT NOT NULL UNIQUE,
		created_at   INTEGER NOT NULL,
		revoked_at   INTEGER,
		last_seen_at INTEGER
	);
	CREATE TABLE audit (
		id     INTEGER PRIMARY KEY,
		ts     INTEGER NOT NULL,
		ip     TEXT NOT NULL,
		event  TEXT NOT NULL,
		detail TEXT NOT NULL DEFAULT ''
	);`,
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	err := s.db.QueryRowContext(ctx, `SELECT v FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := s.db.ExecContext(ctx, `INSERT INTO schema_version (v) VALUES (0)`); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE schema_version SET v = ?`, i+1); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

// --- settings ---

// Setting keys.
const (
	KeyPasswordHash = "password_hash"
	KeyTOTPSecret   = "totp_secret"
	KeyTOTPLastStep = "totp_last_step"
)

// GetSetting returns a setting or ErrNotFound.
func (s *Store) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return v, err
}

// SetSetting upserts a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

// SetCredentials atomically replaces password, TOTP secret and recovery codes
// and drops all sessions. Used by `srvmon-server init`.
func (s *Store) SetCredentials(ctx context.Context, passwordHash, totpSecret string, recoveryHashes []string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmts := []struct {
		q    string
		args []any
	}{
		{`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, []any{KeyPasswordHash, passwordHash}},
		{`INSERT INTO settings (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, []any{KeyTOTPSecret, totpSecret}},
		{`DELETE FROM settings WHERE key = ?`, []any{KeyTOTPLastStep}},
		{`DELETE FROM recovery_codes`, nil},
		{`DELETE FROM sessions`, nil},
	}
	for _, st := range stmts {
		if _, err := tx.ExecContext(ctx, st.q, st.args...); err != nil {
			return err
		}
	}
	for _, h := range recoveryHashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (code_hash) VALUES (?)`, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// UseRecoveryCode marks an unused recovery code as used. Returns false if the
// code does not exist or was already used.
func (s *Store) UseRecoveryCode(ctx context.Context, codeHash string) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		`UPDATE recovery_codes SET used_at = ? WHERE code_hash = ? AND used_at IS NULL`, time.Now().Unix(), codeHash)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// --- sessions ---

// Session kinds.
const (
	SessionPending = "pending"
	SessionFull    = "full"
)

// CreateSession stores a session by the hash of its id.
func (s *Store) CreateSession(ctx context.Context, idHash, kind, ip string, ttl time.Duration) error {
	now := time.Now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, kind, ip, created_at, expires_at) VALUES (?, ?, ?, ?, ?)`,
		idHash, kind, ip, now.Unix(), now.Add(ttl).Unix())
	return err
}

// SessionKind returns the kind of a live session or ErrNotFound.
func (s *Store) SessionKind(ctx context.Context, idHash string) (string, error) {
	var kind string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind FROM sessions WHERE id_hash = ? AND expires_at > ?`, idHash, time.Now().Unix()).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return kind, err
}

// DeleteSession removes a session.
func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// PurgeExpiredSessions deletes expired sessions.
func (s *Store) PurgeExpiredSessions(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, time.Now().Unix())
	return err
}

// --- audit ---

// Audit appends an audit record.
func (s *Store) Audit(ctx context.Context, ip, event, detail string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (ts, ip, event, detail) VALUES (?, ?, ?, ?)`, time.Now().Unix(), ip, event, detail)
	return err
}

// --- servers & agent tokens ---

// Server is a monitored server card.
type Server struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	Description string     `json:"description"`
	Project     string     `json:"project"`
	Group       string     `json:"group"`
	Tags        []string   `json:"tags"`
	Host        string     `json:"host"`
	Port        int        `json:"port"`
	Provider    string     `json:"provider"`
	Plan        string     `json:"plan"`
	Cost        *float64   `json:"cost,omitempty"`
	Currency    string     `json:"currency"`
	NextPayment string     `json:"next_payment"` // YYYY-MM-DD
	CreatedAt   time.Time  `json:"created_at"`
	LastSeen    *time.Time `json:"last_seen,omitempty"`
}

// CreateServer inserts a server and returns its id.
func (s *Store) CreateServer(ctx context.Context, srv Server) (int64, error) {
	if srv.Tags == nil {
		srv.Tags = []string{}
	}
	if srv.Port == 0 {
		srv.Port = 22
	}
	tags, _ := json.Marshal(srv.Tags)
	res, err := s.db.ExecContext(ctx, `INSERT INTO servers
		(name, description, project, grp, tags, host, port, provider, plan, cost, currency, next_payment, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		srv.Name, srv.Description, srv.Project, srv.Group, string(tags), srv.Host, srv.Port,
		srv.Provider, srv.Plan, srv.Cost, srv.Currency, srv.NextPayment, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

const serverCols = `s.id, s.name, s.description, s.project, s.grp, s.tags, s.host, s.port,
	s.provider, s.plan, s.cost, s.currency, s.next_payment, s.created_at,
	(SELECT MAX(last_seen_at) FROM agent_tokens t WHERE t.server_id = s.id)`

type scanner interface{ Scan(dest ...any) error }

func scanServer(row scanner) (Server, error) {
	var srv Server
	var tags string
	var created int64
	var lastSeen sql.NullInt64
	var cost sql.NullFloat64
	if err := row.Scan(&srv.ID, &srv.Name, &srv.Description, &srv.Project, &srv.Group, &tags, &srv.Host, &srv.Port,
		&srv.Provider, &srv.Plan, &cost, &srv.Currency, &srv.NextPayment, &created, &lastSeen); err != nil {
		return srv, err
	}
	_ = json.Unmarshal([]byte(tags), &srv.Tags)
	if srv.Tags == nil {
		srv.Tags = []string{}
	}
	srv.CreatedAt = time.Unix(created, 0).UTC()
	if cost.Valid {
		c := cost.Float64
		srv.Cost = &c
	}
	if lastSeen.Valid {
		t := time.Unix(lastSeen.Int64, 0).UTC()
		srv.LastSeen = &t
	}
	return srv, nil
}

// ListServers returns all servers ordered by group and name, with the most
// recent agent push time.
func (s *Store) ListServers(ctx context.Context) ([]Server, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serverCols+` FROM servers s ORDER BY s.grp, s.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Server{}
	for rows.Next() {
		srv, err := scanServer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, srv)
	}
	return out, rows.Err()
}

// GetServer returns one server or ErrNotFound.
func (s *Store) GetServer(ctx context.Context, id int64) (Server, error) {
	srv, err := scanServer(s.db.QueryRowContext(ctx, `SELECT `+serverCols+` FROM servers s WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return srv, ErrNotFound
	}
	return srv, err
}

// ServerCard is the editable part of a server ("passport"). The name is not
// editable here: it labels the server's time series.
type ServerCard struct {
	Description string   `json:"description"`
	Project     string   `json:"project"`
	Group       string   `json:"group"`
	Tags        []string `json:"tags"`
	Provider    string   `json:"provider"`
	Plan        string   `json:"plan"`
	Cost        *float64 `json:"cost"`
	Currency    string   `json:"currency"`
	NextPayment string   `json:"next_payment"`
}

// UpdateServerCard replaces the passport fields of a server.
func (s *Store) UpdateServerCard(ctx context.Context, id int64, c ServerCard) error {
	if c.Tags == nil {
		c.Tags = []string{}
	}
	tags, _ := json.Marshal(c.Tags)
	res, err := s.db.ExecContext(ctx, `UPDATE servers SET description = ?, project = ?, grp = ?, tags = ?,
		provider = ?, plan = ?, cost = ?, currency = ?, next_payment = ? WHERE id = ?`,
		c.Description, c.Project, c.Group, string(tags), c.Provider, c.Plan, c.Cost, c.Currency, c.NextPayment, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// AddAgentToken stores the hash of a new agent token for a server.
func (s *Store) AddAgentToken(ctx context.Context, serverID int64, tokenHash string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agent_tokens (server_id, token_hash, created_at) VALUES (?, ?, ?)`, serverID, tokenHash, time.Now().Unix())
	return err
}

// AgentServer resolves a live agent token to its server and bumps last_seen.
func (s *Store) AgentServer(ctx context.Context, tokenHash string) (id int64, name string, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT s.id, s.name FROM agent_tokens t JOIN servers s ON s.id = t.server_id
		WHERE t.token_hash = ? AND t.revoked_at IS NULL`, tokenHash).Scan(&id, &name)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", err
	}
	_, err = s.db.ExecContext(ctx, `UPDATE agent_tokens SET last_seen_at = ? WHERE token_hash = ?`, time.Now().Unix(), tokenHash)
	return id, name, err
}

// ServerIDByName returns the id of the server with this name.
func (s *Store) ServerIDByName(ctx context.Context, name string) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `SELECT id FROM servers WHERE name = ?`, name).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrNotFound
	}
	return id, err
}

// AgentTokenValid reports whether a live (not revoked) token with this hash
// exists, without touching last_seen.
func (s *Store) AgentTokenValid(ctx context.Context, tokenHash string) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agent_tokens WHERE token_hash = ? AND revoked_at IS NULL`, tokenHash).Scan(&n)
	return n > 0, err
}
