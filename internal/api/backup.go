package api

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/backup"
	"github.com/askidmobile/srvmon/internal/i18n"
	"github.com/askidmobile/srvmon/internal/secret"
	"github.com/askidmobile/srvmon/internal/store"
)

// Backups of the panel database to S3 (internal/backup) and the export of
// the master key, which backups never contain.

func (s *Server) backupRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/backup", s.handleBackup)
	h("PUT /api/backup", s.handleBackupSave)
	h("POST /api/backup/test", s.handleBackupTest)
	h("POST /api/backup/run", s.handleBackupRun)
	h("GET /api/backup/list", s.handleBackupList)
	h("POST /api/backup/master-key", s.handleMasterKeyExport)
}

type backupView struct {
	Config        backup.Config `json:"config"`
	Saved         bool          `json:"saved"`
	HasSecret     bool          `json:"has_secret"`
	Status        backup.Status `json:"status"`
	KeyExportedAt *time.Time    `json:"key_exported_at,omitempty"`
}

func (s *Server) backupReady(w http.ResponseWriter) bool {
	if s.backup == nil {
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
		return false
	}
	return true
}

func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	if !s.backupReady(w) {
		return
	}
	c, saved, hasSecret, err := s.backup.Load(r.Context())
	if err != nil {
		s.log.Error("backup: load", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	v := backupView{Config: c, Saved: saved, HasSecret: hasSecret, Status: s.backup.Status(r.Context())}
	if ts, err := s.store.GetSetting(r.Context(), backup.SettingKeyExported); err == nil {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			v.KeyExportedAt = &t
		}
	}
	writeJSON(w, http.StatusOK, v)
}

type backupInput struct {
	Config backup.Config `json:"config"`
	// SecretKey: nil or "" keeps the stored S3 secret.
	SecretKey *string `json:"secret_key"`
}

func (in *backupInput) secret() *string {
	if in.SecretKey == nil || strings.TrimSpace(*in.SecretKey) == "" {
		return nil
	}
	k := strings.TrimSpace(*in.SecretKey)
	return &k
}

func (s *Server) handleBackupSave(w http.ResponseWriter, r *http.Request) {
	if !s.backupReady(w) {
		return
	}
	var in backupInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if field, ok := in.Config.Normalize(); !ok {
		writeError(w, http.StatusBadRequest, field)
		return
	}
	sec := in.secret()
	if sec != nil && len(*sec) > 512 {
		writeError(w, http.StatusBadRequest, "secret_key")
		return
	}
	if _, _, hasSecret, _ := s.backup.Load(r.Context()); sec == nil && !hasSecret {
		writeError(w, http.StatusBadRequest, "secret_key")
		return
	}
	if err := s.backup.Save(r.Context(), in.Config, sec); err != nil {
		s.log.Error("backup: save", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "backup_settings", in.Config.S3.Endpoint+"/"+in.Config.S3.Bucket)
	s.handleBackup(w, r)
}

func (s *Server) handleBackupTest(w http.ResponseWriter, r *http.Request) {
	if !s.backupReady(w) {
		return
	}
	var in backupInput
	if err := readJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	if field, ok := in.Config.Normalize(); !ok {
		writeError(w, http.StatusBadRequest, field)
		return
	}
	key := ""
	if sec := in.secret(); sec != nil {
		key = *sec
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	if err := s.backup.Test(ctx, in.Config.S3, key); err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleBackupRun(w http.ResponseWriter, r *http.Request) {
	if !s.backupReady(w) {
		return
	}
	// Not tied to the request: closing the page must not cut an upload.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 15*time.Minute)
	defer cancel()
	st, err := s.backup.RunNow(ctx)
	_ = s.store.Audit(r.Context(), s.clientIP(r), "backup_run", st.Object)
	switch {
	case errors.Is(err, backup.ErrBusy):
		writeError(w, http.StatusConflict, "busy")
	case errors.Is(err, backup.ErrNotConfigured):
		writeError(w, http.StatusBadRequest, "not_configured")
	default:
		// A failed backup is a result, not a request error: the status
		// carries the reason.
		writeJSON(w, http.StatusOK, st)
	}
}

func (s *Server) handleBackupList(w http.ResponseWriter, r *http.Request) {
	if !s.backupReady(w) {
		return
	}
	c, saved, _, err := s.backup.Load(r.Context())
	if err != nil || !saved {
		writeJSON(w, http.StatusOK, map[string]any{"backups": []backup.Object{}})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	t, err := s.backup.Target(ctx, c.S3, "")
	var objs []backup.Object
	if err == nil {
		objs, err = backup.Backups(ctx, t, c.S3.Prefix)
	}
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"backups": []backup.Object{}, "error": err.Error()})
		return
	}
	if objs == nil {
		objs = []backup.Object{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"backups": objs})
}

// handleMasterKeyExport returns the master key after a fresh code from the
// authenticator (or a recovery code): a session alone is not enough.
func (s *Server) handleMasterKeyExport(w http.ResponseWriter, r *http.Request) {
	ip := s.clientIP(r)
	att, ok := s.reserveAttempt(w, ip)
	if !ok {
		return
	}
	defer att.done()
	var req struct {
		Code string `json:"code"`
	}
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request")
		return
	}
	ok, method, err := s.checkSecondFactor(r.Context(), strings.TrimSpace(req.Code))
	if err != nil {
		s.log.Error("second factor", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	if !ok {
		att.fail()
		s.authFailure(r.Context(), ip, "master_key")
		writeError(w, http.StatusUnauthorized, "invalid_code")
		return
	}
	key, err := secret.ReadKeyFile(s.cfg.MasterKeyFile)
	if err != nil {
		s.log.Error("master key export", "err", err)
		writeError(w, http.StatusServiceUnavailable, "no_master_key")
		return
	}
	defer clear(key)
	now := s.now().UTC()
	_ = s.store.SetSetting(r.Context(), backup.SettingKeyExported, now.Format(time.RFC3339))
	_ = s.store.Audit(r.Context(), ip, "master_key_export", method)
	s.log.Warn("master key exported", "ip", ip)
	s.alerts.Event(r.Context(), store.KindLogin, 0, "", ip, i18n.T("the master key was exported from the panel (IP %s)", ip))
	writeJSON(w, http.StatusOK, map[string]string{"key": base64.StdEncoding.EncodeToString(key)})
}
