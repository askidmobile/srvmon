package backup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

func testStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "srvmon.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func testBox(t *testing.T, b byte) *secret.Box {
	t.Helper()
	box, err := secret.New(bytes.Repeat([]byte{b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func TestCreateOpenRestore(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	if err := st.SetSetting(ctx, "marker", "hello"); err != nil {
		t.Fatal(err)
	}
	master := testBox(t, 1)
	data, m, err := Create(ctx, st, master, "v1.2.3", time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != store.Migrations() || m.DBSize == 0 {
		t.Fatalf("manifest %+v", m)
	}
	if !bytes.HasPrefix(data, []byte(magic)) || bytes.Contains(data, []byte("SQLite format")) || bytes.Contains(data, []byte("hello")) {
		t.Fatal("backup is not encrypted")
	}
	if _, _, err := Open(testBox(t, 2), data); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("other key: %v", err)
	}
	if _, _, err := Open(master, []byte("garbage")); !errors.Is(err, ErrNotBackup) {
		t.Fatalf("garbage: %v", err)
	}
	// A flipped byte must not decrypt.
	bad := append([]byte(nil), data...)
	bad[len(bad)-1] ^= 1
	if _, _, err := Open(master, bad); !errors.Is(err, ErrWrongKey) {
		t.Fatalf("tampered: %v", err)
	}
	m2, db, err := Open(master, data)
	if err != nil {
		t.Fatal(err)
	}
	if m2.Version != "v1.2.3" || int64(len(db)) != m.DBSize {
		t.Fatalf("manifest %+v, db %d", m2, len(db))
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "srvmon.db")
	if err := os.WriteFile(dbPath, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreDB(dbPath, db, false, time.Now()); err == nil {
		t.Fatal("restore over an existing database without force")
	}
	kept, err := RestoreDB(dbPath, db, true, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(kept); string(b) != "old" {
		t.Fatalf("old database not kept: %q", b)
	}
	restored, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	if v, err := restored.GetSetting(ctx, "marker"); err != nil || v != "hello" {
		t.Fatalf("restored marker %q %v", v, err)
	}
}

// memTarget is an in-memory bucket.
type memTarget struct {
	mu      sync.Mutex
	objs    map[string][]byte
	failPut bool
}

func (m *memTarget) Put(_ context.Context, key string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.failPut {
		return errors.New("S3 AccessDenied: Access Denied")
	}
	m.objs[key] = append([]byte(nil), data...)
	return nil
}

func (m *memTarget) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	if !ok {
		return nil, errors.New("S3 NoSuchKey")
	}
	return b, nil
}

func (m *memTarget) List(_ context.Context, prefix string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Object
	for k, v := range m.objs {
		if strings.HasPrefix(k, prefix) {
			out = append(out, Object{Key: k, Size: int64(len(v))})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

func (m *memTarget) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, key)
	return nil
}

func TestRunnerScheduleRetentionAndAlerts(t *testing.T) {
	ctx := context.Background()
	st := testStore(t)
	r := NewRunner(st, testBox(t, 1), slog.New(slog.NewTextHandler(io.Discard, nil)), "test")
	bucket := &memTarget{objs: map[string][]byte{"srvmon/unrelated.txt": []byte("x")}}
	var gotSecret string
	r.NewTarget = func(c S3, sk string) (Target, error) { gotSecret = sk; return bucket, nil }
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	r.Now = func() time.Time { return now }
	var alerts []string
	r.OnFailure = func(_ context.Context, msg string) { alerts = append(alerts, msg) }

	if _, err := r.RunNow(ctx); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("not configured: %v", err)
	}
	c := Config{Enabled: true, S3: S3{Endpoint: "storage.example.com", Bucket: "my-backups", Prefix: "srvmon", AccessKey: "AK"}, Keep: 3}
	if f, ok := c.Normalize(); !ok {
		t.Fatalf("normalize: %s", f)
	}
	if c.S3.Prefix != "srvmon/" || c.IntervalHours != DefaultInterval {
		t.Fatalf("defaults %+v", c)
	}
	sk := "s3cr3t"
	if err := r.Save(ctx, c, &sk); err != nil {
		t.Fatal(err)
	}
	if v, _ := st.GetSetting(ctx, settingSecret); strings.Contains(v, sk) || v == "" {
		t.Fatal("secret stored in clear")
	}

	// Five daily runs keep the newest three.
	for i := 0; i < 5; i++ {
		r.tick(ctx)
		r.tick(ctx) // not due again within the interval
		now = now.Add(24 * time.Hour)
	}
	if gotSecret != sk {
		t.Fatalf("secret %q", gotSecret)
	}
	objs, _ := Backups(ctx, bucket, "srvmon/")
	if len(objs) != 3 || objs[0].Key != "srvmon/srvmon-20261105T000000Z"+Ext {
		t.Fatalf("retention: %+v", objs)
	}
	if _, ok := bucket.objs["srvmon/unrelated.txt"]; !ok {
		t.Fatal("retention deleted a foreign object")
	}
	if s, err := r.RunNow(ctx); err != nil || s.Running {
		t.Fatalf("finished backup reports %+v %v", s, err)
	}
	objs, _ = Backups(ctx, bucket, "srvmon/")
	if s := r.Status(ctx); s.Error != "" || s.OKAt == nil || s.Running || s.Object != objs[0].Key {
		t.Fatalf("status %+v", s)
	}

	// Failures: retried hourly, reported once a day.
	bucket.failPut = true
	for i := 0; i < 54; i++ { // the first failure is due a day after the last backup
		r.tick(ctx)
		now = now.Add(time.Hour)
	}
	if len(alerts) != 2 {
		t.Fatalf("alerts %d: %v", len(alerts), alerts)
	}
	if s := r.Status(ctx); !strings.Contains(s.Error, "AccessDenied") {
		t.Fatalf("status %+v", s)
	}
	bucket.failPut = false
	r.tick(ctx)
	if s := r.Status(ctx); s.Error != "" || s.NotifiedAt != nil {
		t.Fatalf("after recovery %+v", s)
	}

	// Test round-trips an object and leaves nothing behind.
	n := len(bucket.objs)
	if err := r.Test(ctx, c.S3, ""); err != nil {
		t.Fatal(err)
	}
	if len(bucket.objs) != n {
		t.Fatal("test object left behind")
	}
}

func TestS3Validate(t *testing.T) {
	ok := S3{Endpoint: "https://storage.yandexcloud.net", Bucket: "srvmon-backup", AccessKey: "AK", Region: "ru-central1"}
	if f, valid := ok.Validate(); !valid {
		t.Fatalf("valid config rejected: %s", f)
	}
	for _, c := range []struct {
		field string
		mod   func(*S3)
	}{
		{"endpoint", func(s *S3) { s.Endpoint = "ftp://x" }},
		{"endpoint", func(s *S3) { s.Endpoint = "https://x/path" }},
		{"endpoint", func(s *S3) { s.Endpoint = "" }},
		{"bucket", func(s *S3) { s.Bucket = "Bad_Bucket" }},
		{"prefix", func(s *S3) { s.Prefix = "../x/" }},
		{"access_key", func(s *S3) { s.AccessKey = "" }},
	} {
		s := ok
		c.mod(&s)
		if f, valid := s.Validate(); valid || f != c.field {
			t.Errorf("%+v: got %q %v, want %q", s, f, valid, c.field)
		}
	}
}
