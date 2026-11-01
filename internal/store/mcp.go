package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// MCP access tokens: Claude Desktop, Cursor and other MCP clients reach the
// tool API at /mcp with one of these instead of the password + TOTP login.
// Only the sha256 of a token is kept; Prefix is its first characters, shown
// so the owner can tell tokens apart. Deleting the row revokes the token.
//
// agent_actions.source tells who asked for a command: empty for the built-in
// agent, "mcp:<token name>" for an MCP client.
const migrationMCP = `CREATE TABLE mcp_tokens (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		name         TEXT NOT NULL,
		token_hash   TEXT NOT NULL UNIQUE,
		prefix       TEXT NOT NULL,
		scopes       TEXT NOT NULL DEFAULT '[]', -- JSON []string
		created_at   INTEGER NOT NULL,
		expires_at   INTEGER,
		last_used_at INTEGER,
		last_ip      TEXT NOT NULL DEFAULT ''
	);
	ALTER TABLE agent_actions ADD COLUMN source TEXT NOT NULL DEFAULT '';`

// MCP token scopes.
const (
	ScopeRead  = "read"  // servers, metrics, alerts, tasks, own actions
	ScopeExec  = "exec"  // commands; changing ones still wait for the owner
	ScopeTasks = "tasks" // create and delete scheduled agent tasks
)

// MCPScopes lists the valid scopes in display order.
var MCPScopes = []string{ScopeRead, ScopeExec, ScopeTasks}

// MCPToken is an MCP access token without its secret.
type MCPToken struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"created_at"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	LastIP     string     `json:"last_ip,omitempty"`
}

// Has reports whether the token carries scope.
func (t MCPToken) Has(scope string) bool {
	for _, s := range t.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

const mcpCols = `id, name, prefix, scopes, created_at, expires_at, last_used_at, last_ip`

func scanMCPToken(row scanner) (MCPToken, error) {
	var t MCPToken
	var scopes string
	var created int64
	var expires, used sql.NullInt64
	if err := row.Scan(&t.ID, &t.Name, &t.Prefix, &scopes, &created, &expires, &used, &t.LastIP); err != nil {
		return t, err
	}
	t.Scopes = []string{}
	_ = json.Unmarshal([]byte(scopes), &t.Scopes)
	t.CreatedAt = time.Unix(created, 0).UTC()
	if expires.Valid {
		v := time.Unix(expires.Int64, 0).UTC()
		t.ExpiresAt = &v
	}
	if used.Valid {
		v := time.Unix(used.Int64, 0).UTC()
		t.LastUsedAt = &v
	}
	return t, nil
}

// CreateMCPToken stores a new token by its hash.
func (s *Store) CreateMCPToken(ctx context.Context, t MCPToken, hash string) (MCPToken, error) {
	scopes, _ := json.Marshal(t.Scopes)
	var expires any
	if t.ExpiresAt != nil {
		expires = t.ExpiresAt.Unix()
	}
	res, err := s.db.ExecContext(ctx, `INSERT INTO mcp_tokens (name, token_hash, prefix, scopes, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`, t.Name, hash, t.Prefix, string(scopes), time.Now().Unix(), expires)
	if err != nil {
		return t, err
	}
	id, _ := res.LastInsertId()
	return scanMCPToken(s.db.QueryRowContext(ctx, `SELECT `+mcpCols+` FROM mcp_tokens WHERE id = ?`, id))
}

// ListMCPTokens returns all tokens, newest first.
func (s *Store) ListMCPTokens(ctx context.Context) ([]MCPToken, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+mcpCols+` FROM mcp_tokens ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MCPToken{}
	for rows.Next() {
		t, err := scanMCPToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MCPTokenByHash returns the token with this hash if it has not expired,
// else ErrNotFound.
func (s *Store) MCPTokenByHash(ctx context.Context, hash string, now time.Time) (MCPToken, error) {
	t, err := scanMCPToken(s.db.QueryRowContext(ctx, `SELECT `+mcpCols+` FROM mcp_tokens
		WHERE token_hash = ? AND (expires_at IS NULL OR expires_at > ?)`, hash, now.Unix()))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// TouchMCPToken records a use of the token.
func (s *Store) TouchMCPToken(ctx context.Context, id int64, ip string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE mcp_tokens SET last_used_at = ?, last_ip = ? WHERE id = ?`, at.Unix(), ip, id)
	return err
}

// DeleteMCPToken revokes a token. It returns ErrNotFound for an unknown id.
func (s *Store) DeleteMCPToken(ctx context.Context, id int64) (MCPToken, error) {
	t, err := scanMCPToken(s.db.QueryRowContext(ctx, `SELECT `+mcpCols+` FROM mcp_tokens WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	} else if err != nil {
		return t, err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM mcp_tokens WHERE id = ?`, id)
	return t, err
}
