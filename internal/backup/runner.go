package backup

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

// Settings keys.
const (
	settingConfig = "backup.config"
	settingSecret = "backup.s3_secret" // sealed with the master key
	settingStatus = "backup.status"
	// SettingKeyExported holds when the owner last exported the master key.
	SettingKeyExported = "backup.key_exported"
)

var secretAAD = []byte("srvmon backup s3 secret")

// Config is the backup setup kept in the panel. The S3 secret is stored
// apart, sealed with the master key.
type Config struct {
	Enabled       bool `json:"enabled"`
	S3            S3   `json:"s3"`
	IntervalHours int  `json:"interval_hours"` // 1..168
	Keep          int  `json:"keep"`           // backups kept in the bucket, 1..365
}

// Defaults.
const (
	DefaultInterval = 24
	DefaultKeep     = 14
)

// Normalize fills defaults and checks the ranges; it returns the bad field.
func (c *Config) Normalize() (string, bool) {
	if c.IntervalHours == 0 {
		c.IntervalHours = DefaultInterval
	}
	if c.Keep == 0 {
		c.Keep = DefaultKeep
	}
	c.S3.Endpoint = strings.TrimSpace(c.S3.Endpoint)
	c.S3.Bucket = strings.TrimSpace(c.S3.Bucket)
	c.S3.Prefix = strings.TrimSpace(c.S3.Prefix)
	c.S3.Region = strings.TrimSpace(c.S3.Region)
	c.S3.AccessKey = strings.TrimSpace(c.S3.AccessKey)
	if c.S3.Prefix != "" && !strings.HasSuffix(c.S3.Prefix, "/") {
		c.S3.Prefix += "/"
	}
	if c.IntervalHours < 1 || c.IntervalHours > 168 {
		return "interval_hours", false
	}
	if c.Keep < 1 || c.Keep > 365 {
		return "keep", false
	}
	return c.S3.Validate()
}

// Status is the outcome of the last backup attempts.
type Status struct {
	AttemptAt *time.Time `json:"attempt_at,omitempty"`
	OKAt      *time.Time `json:"ok_at,omitempty"` // last success
	Object    string     `json:"object,omitempty"`
	Size      int64      `json:"size,omitempty"`
	Error     string     `json:"error,omitempty"` // of the last attempt; "" = it succeeded
	// NotifiedAt: when a failure was last reported, to report a lasting
	// failure once a day and not on every retry.
	NotifiedAt *time.Time `json:"notified_at,omitempty"`
	Running    bool       `json:"running"`
}

// Runner makes backups on schedule and on request.
type Runner struct {
	st      *store.Store
	box     *secret.Box
	log     *slog.Logger
	version string
	// OnFailure reports a failed scheduled backup (alert event).
	OnFailure func(ctx context.Context, msg string)
	// NewTarget opens the storage; tests replace it.
	NewTarget func(S3, string) (Target, error)
	Now       func() time.Time

	mu      sync.Mutex // one backup at a time
	running bool
}

// NewRunner builds a runner. box is the master key.
func NewRunner(st *store.Store, box *secret.Box, log *slog.Logger, version string) *Runner {
	return &Runner{st: st, box: box, log: log, version: version, NewTarget: NewS3, Now: time.Now}
}

// Load returns the config, whether it was saved and whether a secret is set.
func (r *Runner) Load(ctx context.Context) (Config, bool, bool, error) {
	var c Config
	v, err := r.st.GetSetting(ctx, settingConfig)
	if errors.Is(err, store.ErrNotFound) {
		c.IntervalHours, c.Keep = DefaultInterval, DefaultKeep
		return c, false, false, nil
	}
	if err != nil {
		return c, false, false, err
	}
	if err := json.Unmarshal([]byte(v), &c); err != nil {
		return c, false, false, err
	}
	s, _ := r.st.GetSetting(ctx, settingSecret)
	return c, true, s != "", nil
}

func (r *Runner) secret(ctx context.Context) (string, error) {
	v, err := r.st.GetSetting(ctx, settingSecret)
	if err != nil || v == "" {
		return "", errors.New("no S3 secret key saved")
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}
	pt, err := r.box.Open(raw, secretAAD)
	if err != nil {
		return "", errors.New("the S3 secret key does not open with this master key: enter it again")
	}
	return string(pt), nil
}

// Save stores the config; secretKey == nil keeps the stored secret.
func (r *Runner) Save(ctx context.Context, c Config, secretKey *string) error {
	if secretKey != nil {
		sealed, err := r.box.Seal([]byte(*secretKey), secretAAD)
		if err != nil {
			return err
		}
		if err := r.st.SetSetting(ctx, settingSecret, base64.StdEncoding.EncodeToString(sealed)); err != nil {
			return err
		}
	}
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return r.st.SetSetting(ctx, settingConfig, string(b))
}

// Status returns the stored status.
func (r *Runner) Status(ctx context.Context) Status {
	var s Status
	if v, err := r.st.GetSetting(ctx, settingStatus); err == nil {
		_ = json.Unmarshal([]byte(v), &s)
	}
	r.mu.Lock()
	s.Running = r.running
	r.mu.Unlock()
	return s
}

func (r *Runner) saveStatus(ctx context.Context, s Status) {
	s.Running = false
	b, _ := json.Marshal(s)
	if err := r.st.SetSetting(ctx, settingStatus, string(b)); err != nil {
		r.log.Error("backup: save status", "err", err)
	}
}

// Target opens the storage of c; secretKey "" takes the stored one.
func (r *Runner) Target(ctx context.Context, c S3, secretKey string) (Target, error) {
	if secretKey == "" {
		var err error
		if secretKey, err = r.secret(ctx); err != nil {
			return nil, err
		}
	}
	return r.NewTarget(c, secretKey)
}

// Test writes, reads back and deletes a small object: the key needs all
// three rights (delete for the retention).
func (r *Runner) Test(ctx context.Context, c S3, secretKey string) error {
	t, err := r.Target(ctx, c, secretKey)
	if err != nil {
		return err
	}
	key := c.Prefix + ".srvmon-write-test"
	payload := []byte("srvmon " + r.Now().UTC().Format(time.RFC3339))
	if err := t.Put(ctx, key, payload); err != nil {
		return err
	}
	got, err := t.Get(ctx, key)
	if err != nil {
		return err
	}
	if string(got) != string(payload) {
		return errors.New("the test object came back different")
	}
	if err := t.Delete(ctx, key); err != nil {
		return err
	}
	_, err = t.List(ctx, c.Prefix)
	return err
}

// ErrBusy: a backup is already running.
var ErrBusy = errors.New("a backup is already running")

// ErrNotConfigured: no storage is set up.
var ErrNotConfigured = errors.New("backups are not set up")

// RunNow makes a backup, uploads it and applies the retention.
func (r *Runner) RunNow(ctx context.Context) (Status, error) {
	r.mu.Lock()
	if r.running {
		r.mu.Unlock()
		return r.Status(ctx), ErrBusy
	}
	r.running = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.running = false
		r.mu.Unlock()
	}()

	st := r.Status(ctx)
	now := r.Now().UTC()
	st.AttemptAt = &now
	// The returned status describes the finished attempt.
	st.Running = false
	obj, size, err := r.run(ctx, now)
	if err != nil {
		st.Error = err.Error()
		r.saveStatus(ctx, st)
		r.log.Warn("backup failed", "err", err)
		return st, err
	}
	st.Error, st.OKAt, st.Object, st.Size, st.NotifiedAt = "", &now, obj, size, nil
	r.saveStatus(ctx, st)
	r.log.Info("backup ok", "object", obj, "size", size)
	return st, nil
}

func (r *Runner) run(ctx context.Context, now time.Time) (string, int64, error) {
	c, saved, _, err := r.Load(ctx)
	if err != nil {
		return "", 0, err
	}
	if !saved {
		return "", 0, ErrNotConfigured
	}
	t, err := r.Target(ctx, c.S3, "")
	if err != nil {
		return "", 0, err
	}
	data, _, err := Create(ctx, r.st, r.box, r.version, now)
	if err != nil {
		return "", 0, err
	}
	key := ObjectName(c.S3.Prefix, now)
	if err := t.Put(ctx, key, data); err != nil {
		return "", 0, err
	}
	if err := r.prune(ctx, t, c); err != nil {
		// The backup itself is in place; an old copy stays a while longer.
		r.log.Warn("backup: retention", "err", err)
	}
	return key, int64(len(data)), nil
}

func (r *Runner) prune(ctx context.Context, t Target, c Config) error {
	objs, err := Backups(ctx, t, c.S3.Prefix)
	if err != nil {
		return err
	}
	for i := c.Keep; i < len(objs); i++ {
		if err := t.Delete(ctx, objs[i].Key); err != nil {
			return err
		}
	}
	return nil
}

// due reports whether a scheduled backup should run now.
func due(c Config, s Status, now time.Time) bool {
	if !c.Enabled {
		return false
	}
	if s.AttemptAt == nil {
		return true
	}
	wait := time.Duration(c.IntervalHours) * time.Hour
	if s.Error != "" && wait > time.Hour {
		// Retry a failed backup hourly.
		wait = time.Hour
	}
	return !now.Before(s.AttemptAt.Add(wait))
}

// Run makes scheduled backups until ctx ends.
func (r *Runner) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			r.tick(ctx)
		}
	}
}

func (r *Runner) tick(ctx context.Context) {
	c, saved, _, err := r.Load(ctx)
	if err != nil || !saved {
		return
	}
	if !due(c, r.Status(ctx), r.Now()) {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, 30*time.Minute)
	defer cancel()
	st, err := r.RunNow(bctx)
	if err == nil || errors.Is(err, ErrBusy) {
		return
	}
	now := r.Now().UTC()
	if st.NotifiedAt != nil && now.Sub(*st.NotifiedAt) < 24*time.Hour {
		return
	}
	if r.OnFailure != nil {
		r.OnFailure(ctx, err.Error())
	}
	st.NotifiedAt = &now
	r.saveStatus(ctx, st)
}
