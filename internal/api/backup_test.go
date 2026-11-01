package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/askidmobile/srvmon/internal/backup"
	"github.com/askidmobile/srvmon/internal/sshgw"
)

type memBucket struct {
	mu   sync.Mutex
	objs map[string][]byte
}

func (m *memBucket) Put(_ context.Context, k string, d []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objs[k] = d
	return nil
}

func (m *memBucket) Get(_ context.Context, k string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d, ok := m.objs[k]; ok {
		return d, nil
	}
	return nil, errors.New("S3 NoSuchKey")
}

func (m *memBucket) List(_ context.Context, p string) ([]backup.Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []backup.Object
	for k, d := range m.objs {
		if strings.HasPrefix(k, p) {
			out = append(out, backup.Object{Key: k, Size: int64(len(d))})
		}
	}
	return out, nil
}

func (m *memBucket) Delete(_ context.Context, k string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objs, k)
	return nil
}

func TestBackupAPI(t *testing.T) {
	e := newEnv(t)
	if r := e.get(t, "/api/backup"); r.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no session: %d", r.StatusCode)
	}
	e.loginSSH(t)
	if r := e.get(t, "/api/backup"); r.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("no runner: %d", r.StatusCode)
	}
	bucket := &memBucket{objs: map[string][]byte{}}
	var secrets []string
	e.srv.backup = backup.NewRunner(e.st, e.srv.secrets, e.srv.log, "test")
	e.srv.backup.NewTarget = func(_ backup.S3, sk string) (backup.Target, error) {
		secrets = append(secrets, sk)
		return bucket, nil
	}

	cfg := map[string]any{"enabled": true, "s3": map[string]any{
		"endpoint": "storage.example.com", "bucket": "panel-backups", "prefix": "srvmon", "access_key": "AK"}}
	// No secret stored yet: it is required.
	if r := e.do(t, "PUT", "/api/backup", map[string]any{"config": cfg}); r.StatusCode != http.StatusBadRequest {
		t.Fatalf("save without secret: %d", r.StatusCode)
	}
	bad := map[string]any{"config": map[string]any{"s3": map[string]any{"endpoint": "x", "bucket": "B!", "access_key": "AK"}}, "secret_key": "s"}
	var errBody map[string]string
	if code := e.postJSON(t, "/api/backup/test", bad, &errBody); code != http.StatusBadRequest || errBody["error"] != "bucket" {
		t.Fatalf("bad bucket: %d %v", code, errBody)
	}
	var test map[string]any
	if code := e.postJSON(t, "/api/backup/test", map[string]any{"config": cfg, "secret_key": "typed"}, &test); code != 200 || test["ok"] != true {
		t.Fatalf("test: %d %v", code, test)
	}
	r := e.do(t, "PUT", "/api/backup", map[string]any{"config": cfg, "secret_key": "stored-secret"})
	var v backupView
	_ = json.NewDecoder(r.Body).Decode(&v)
	if r.StatusCode != 200 || !v.Saved || !v.HasSecret || v.Config.S3.Prefix != "srvmon/" || v.Config.Keep != backup.DefaultKeep {
		t.Fatalf("save: %d %+v", r.StatusCode, v)
	}
	raw := e.do(t, "GET", "/api/backup", nil)
	var body map[string]any
	_ = json.NewDecoder(raw.Body).Decode(&body)
	if b, _ := json.Marshal(body); strings.Contains(string(b), "stored-secret") {
		t.Fatal("the secret is sent back")
	}

	var st backup.Status
	if code := e.postJSON(t, "/api/backup/run", nil, &st); code != 200 || st.Error != "" || st.Object == "" {
		t.Fatalf("run: %d %+v", code, st)
	}
	if secrets[len(secrets)-1] != "stored-secret" {
		t.Fatalf("secrets used: %v", secrets)
	}
	if _, _, err := backup.Open(e.srv.secrets, bucket.objs[st.Object]); err != nil {
		t.Fatalf("uploaded backup: %v", err)
	}
	lr := e.do(t, "GET", "/api/backup/list", nil)
	var list struct{ Backups []backup.Object }
	_ = json.NewDecoder(lr.Body).Decode(&list)
	if len(list.Backups) != 1 {
		t.Fatalf("list %+v", list)
	}
}

func TestMasterKeyExport(t *testing.T) {
	e := newEnv(t)
	e.loginSSH(t)
	keyFile := filepath.Join(t.TempDir(), "master.key")
	key := []byte(strings.Repeat("k", 32))
	if err := os.WriteFile(keyFile, key, 0o600); err != nil {
		t.Fatal(err)
	}
	e.srv.cfg.MasterKeyFile = keyFile
	var out map[string]string
	if code := e.postJSON(t, "/api/backup/master-key", map[string]string{"code": "000000"}, &out); code != http.StatusUnauthorized || out["key"] != "" {
		t.Fatalf("wrong code: %d %v", code, out)
	}
	if !strings.Contains(e.authLog.String(), "stage=master_key") {
		t.Fatal("failed attempt not in the fail2ban log")
	}
	out = nil
	if code := e.postJSON(t, "/api/backup/master-key", map[string]string{"code": "aaaa-bbbb-cccc"}, &out); code != 200 {
		t.Fatalf("export: %d", code)
	}
	if got, _ := base64.StdEncoding.DecodeString(out["key"]); string(got) != string(key) {
		t.Fatalf("key %q", out["key"])
	}
	if _, err := e.st.GetSetting(context.Background(), backup.SettingKeyExported); err != nil {
		t.Fatal("export not recorded")
	}
}

func TestEmergencyKeysAPI(t *testing.T) {
	a := newAgentEnv(t, "")
	k, _ := sshgw.GenerateKey("owner@laptop")
	var errBody map[string]any
	if code := a.postJSON(t, "/api/access/emergency/apply", nil, nil); code != 200 {
		t.Fatalf("apply: %d", code)
	}
	r := a.do(t, "PUT", "/api/access/emergency", map[string]any{"keys": []string{"not a key"}})
	_ = json.NewDecoder(r.Body).Decode(&errBody)
	if r.StatusCode != http.StatusBadRequest || errBody["error"] != "bad_key" {
		t.Fatalf("bad key: %d %v", r.StatusCode, errBody)
	}
	r = a.do(t, "PUT", "/api/access/emergency", map[string]any{"keys": []string{k.Public, k.Public}})
	var v emergencyView
	_ = json.NewDecoder(r.Body).Decode(&v)
	if r.StatusCode != 200 || len(v.Keys) != 1 || v.Keys[0].Comment != "owner@laptop" {
		t.Fatalf("save: %d %+v", r.StatusCode, v)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rr := a.do(t, "GET", "/api/access/emergency", nil)
		v = emergencyView{}
		_ = json.NewDecoder(rr.Body).Decode(&v)
		if len(v.Servers) == 1 && v.Servers[0].State == "ok" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not applied: %+v", v.Servers)
		}
		time.Sleep(50 * time.Millisecond)
	}
	found := false
	for _, c := range a.sshd.commands() {
		found = found || c == "sh -s"
	}
	if !found {
		t.Fatalf("no script run: %v", a.sshd.commands())
	}
}
