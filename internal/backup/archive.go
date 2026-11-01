// Package backup makes encrypted copies of the panel database, uploads them
// to S3-compatible storage on a schedule and restores them.
//
// A backup is one file: the magic "SRVMONBK", a format byte and a
// gzip-compressed tar (manifest.json + srvmon.db) sealed with
// XChaCha20-Poly1305 under a key derived from the master key. The master key
// itself is never part of a backup: it has to be kept apart (exported once
// from the panel), and without it a backup cannot be read, while the storage
// provider only ever sees ciphertext.
package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

const (
	magic         = "SRVMONBK"
	formatVersion = 1
	// keyLabel derives the backup key from the master key.
	keyLabel = "srvmon backup v1"
	// Ext is the file extension of backups.
	Ext = ".srvmon-backup"
	// maxDB bounds what Open accepts, so a damaged or hostile file cannot
	// exhaust memory.
	maxDB = 2 << 30
)

// Manifest describes a backup.
type Manifest struct {
	Format    int       `json:"format"`
	CreatedAt time.Time `json:"created_at"`
	Version   string    `json:"version"` // srvmon-server build
	Schema    int       `json:"schema"`  // applied migrations
	DBSize    int64     `json:"db_size"`
}

// Key returns the box that seals backups.
func Key(master *secret.Box) *secret.Box { return master.Derive(keyLabel) }

// Create snapshots the database and returns the encrypted backup.
func Create(ctx context.Context, st *store.Store, master *secret.Box, version string, now time.Time) ([]byte, Manifest, error) {
	m := Manifest{Format: formatVersion, CreatedAt: now.UTC(), Version: version}
	schema, err := st.SchemaVersion(ctx)
	if err != nil {
		return nil, m, err
	}
	m.Schema = schema
	dir, err := os.MkdirTemp("", "srvmon-backup-")
	if err != nil {
		return nil, m, err
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "srvmon.db")
	if err := st.Snapshot(ctx, dbPath); err != nil {
		return nil, m, fmt.Errorf("snapshot: %w", err)
	}
	db, err := os.ReadFile(dbPath)
	if err != nil {
		return nil, m, err
	}
	m.DBSize = int64(len(db))
	plain, err := pack(m, db)
	if err != nil {
		return nil, m, err
	}
	out, err := seal(master, plain)
	return out, m, err
}

func pack(m Manifest, db []byte) ([]byte, error) {
	man, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, f := range []struct {
		name string
		data []byte
	}{{"manifest.json", man}, {"srvmon.db", db}} {
		if err := tw.WriteHeader(&tar.Header{Name: f.name, Mode: 0o600, Size: int64(len(f.data)), ModTime: m.CreatedAt}); err != nil {
			return nil, err
		}
		if _, err := tw.Write(f.data); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func header() []byte { return append([]byte(magic), formatVersion) }

func seal(master *secret.Box, plain []byte) ([]byte, error) {
	h := header()
	ct, err := Key(master).Seal(plain, h)
	if err != nil {
		return nil, err
	}
	return append(h, ct...), nil
}

// ErrNotBackup is returned for data that is not a srvmon backup.
var ErrNotBackup = errors.New("not a srvmon backup")

// ErrWrongKey is returned when the backup does not open with this master key.
var ErrWrongKey = errors.New("the backup does not open with this master key (another installation, or a damaged file)")

// Open decrypts a backup and returns its manifest and database.
func Open(master *secret.Box, data []byte) (Manifest, []byte, error) {
	var m Manifest
	h := header()
	if len(data) < len(h) || !bytes.Equal(data[:len(magic)], h[:len(magic)]) {
		return m, nil, ErrNotBackup
	}
	if data[len(magic)] != formatVersion {
		return m, nil, fmt.Errorf("backup format %d is not supported by this build", data[len(magic)])
	}
	plain, err := Key(master).Open(data[len(h):], h)
	if err != nil {
		return m, nil, ErrWrongKey
	}
	gz, err := gzip.NewReader(bytes.NewReader(plain))
	if err != nil {
		return m, nil, err
	}
	tr := tar.NewReader(gz)
	var db []byte
	var haveMan bool
	for {
		hd, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return m, nil, err
		}
		switch hd.Name {
		case "manifest.json":
			b, err := io.ReadAll(io.LimitReader(tr, 1<<20))
			if err != nil {
				return m, nil, err
			}
			if err := json.Unmarshal(b, &m); err != nil {
				return m, nil, fmt.Errorf("manifest: %w", err)
			}
			haveMan = true
		case "srvmon.db":
			if hd.Size > maxDB {
				return m, nil, errors.New("database in the backup is too large")
			}
			if db, err = io.ReadAll(io.LimitReader(tr, maxDB)); err != nil {
				return m, nil, err
			}
		}
	}
	if !haveMan || db == nil {
		return m, nil, errors.New("backup is incomplete")
	}
	return m, db, nil
}

// RestoreDB writes db as the database at dbPath. An existing database is
// kept next to it as <name>.before-restore-<time> (with its -wal/-shm), so a
// restore can be undone by hand. The panel must be stopped.
func RestoreDB(dbPath string, db []byte, force bool, now time.Time) (kept string, err error) {
	if _, err := os.Stat(dbPath); err == nil {
		if !force {
			return "", fmt.Errorf("%s exists: stop srvmon-server and pass -force (the current database is kept as a copy)", dbPath)
		}
		kept = dbPath + ".before-restore-" + now.UTC().Format("20060102T150405Z")
		if err := os.Rename(dbPath, kept); err != nil {
			return "", err
		}
		for _, sfx := range []string{"-wal", "-shm"} {
			if _, err := os.Stat(dbPath + sfx); err == nil {
				if err := os.Rename(dbPath+sfx, kept+sfx); err != nil {
					return kept, err
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return kept, err
	}
	tmp := dbPath + ".restore"
	if err := os.WriteFile(tmp, db, 0o600); err != nil {
		return kept, err
	}
	return kept, os.Rename(tmp, dbPath)
}
