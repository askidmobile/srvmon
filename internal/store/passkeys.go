package store

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Passkeys (WebAuthn) are an optional way into the panel next to the
// password and TOTP. A passkey belongs to the domain it was made on (its
// RP ID): the sign-in page offers it only at that address.

// MaxPasskeys caps the registered passkeys.
const MaxPasskeys = 20

const migrationPasskeys = `CREATE TABLE passkeys (
		id           INTEGER PRIMARY KEY AUTOINCREMENT,
		cred_id      TEXT NOT NULL UNIQUE, -- base64url credential id
		rp_id        TEXT NOT NULL,        -- the domain it was registered on
		name         TEXT NOT NULL DEFAULT '',
		data         TEXT NOT NULL,        -- JSON webauthn.Credential: public key, sign count
		created_at   INTEGER NOT NULL,
		last_used_at INTEGER
	);`

// Passkey is a registered WebAuthn credential. Data holds only public
// material (the public key and the signature counter).
type Passkey struct {
	ID       int64      `json:"id"`
	CredID   string     `json:"-"`
	RPID     string     `json:"rp_id"`
	Name     string     `json:"name"`
	Data     string     `json:"-"`
	Created  time.Time  `json:"created_at"`
	LastUsed *time.Time `json:"last_used_at"`
}

// ListPasskeys returns all passkeys, oldest first.
func (s *Store) ListPasskeys(ctx context.Context) ([]Passkey, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, cred_id, rp_id, name, data, created_at, last_used_at FROM passkeys ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Passkey{}
	for rows.Next() {
		var p Passkey
		var created int64
		var used sql.NullInt64
		if err := rows.Scan(&p.ID, &p.CredID, &p.RPID, &p.Name, &p.Data, &created, &used); err != nil {
			return nil, err
		}
		p.Created = time.Unix(created, 0).UTC()
		if used.Valid {
			t := time.Unix(used.Int64, 0).UTC()
			p.LastUsed = &t
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ErrTooManyPasskeys is returned by AddPasskey at MaxPasskeys.
var ErrTooManyPasskeys = errors.New("store: too many passkeys")

// AddPasskey stores a new passkey.
func (s *Store) AddPasskey(ctx context.Context, p Passkey) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM passkeys`).Scan(&n); err != nil {
		return 0, err
	}
	if n >= MaxPasskeys {
		return 0, ErrTooManyPasskeys
	}
	res, err := tx.ExecContext(ctx,
		`INSERT INTO passkeys (cred_id, rp_id, name, data, created_at) VALUES (?, ?, ?, ?, ?)`,
		p.CredID, p.RPID, p.Name, p.Data, time.Now().Unix())
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

// UsePasskey saves the credential after a sign-in (new signature counter)
// and the time of use.
func (s *Store) UsePasskey(ctx context.Context, credID, data string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE passkeys SET data = ?, last_used_at = ? WHERE cred_id = ?`, data, time.Now().Unix(), credID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePasskey removes a passkey and returns it.
func (s *Store) DeletePasskey(ctx context.Context, id int64) (Passkey, error) {
	var p Passkey
	err := s.db.QueryRowContext(ctx,
		`DELETE FROM passkeys WHERE id = ? RETURNING id, rp_id, name`, id).Scan(&p.ID, &p.RPID, &p.Name)
	if errors.Is(err, sql.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}
