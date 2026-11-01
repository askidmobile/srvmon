package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/askidmobile/srvmon/internal/recording"
	"github.com/askidmobile/srvmon/internal/store"
)

// Terminal session recordings (asciicast v2, sealed with the master key).
// The web terminal records output and resizes, never keystrokes; the owner
// turns recording off, sets how long recordings are kept and how large one
// may grow in settings.

const (
	recDefaultRetention = 30 // days
	recDefaultMaxMB     = 50
	recMaxRetention     = 365
	recMaxMB            = 500
	recListLimit        = 500
	recPageSize         = 100
	recPurgeEvery       = time.Hour
	recOrphanAge        = time.Hour
)

type recSettings struct {
	Enabled       bool `json:"enabled"`
	RetentionDays int  `json:"retention_days"`
	MaxMB         int  `json:"max_mb"`
}

type recSettingsView struct {
	recSettings
	Available bool  `json:"available"` // a data dir and master key are set
	Count     int   `json:"count"`
	DiskBytes int64 `json:"disk_bytes"`
}

// recDir is where recording files live; "" disables recording (no data
// directory configured, as in tests).
func (s *Server) recDir() string {
	if s.cfg.DataDir == "" {
		return ""
	}
	return filepath.Join(s.cfg.DataDir, "recordings")
}

func (s *Server) recPath(fileID string) string { return filepath.Join(s.recDir(), fileID+".rec") }

func (s *Server) loadRecSettings(ctx context.Context) recSettings {
	rs := recSettings{Enabled: true, RetentionDays: recDefaultRetention, MaxMB: recDefaultMaxMB}
	if v, err := s.store.GetSetting(ctx, store.KeyRecEnabled); err == nil {
		rs.Enabled = v != "0"
	}
	if v, err := s.store.GetSetting(ctx, store.KeyRecRetention); err == nil {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= recMaxRetention {
			rs.RetentionDays = n
		}
	}
	if v, err := s.store.GetSetting(ctx, store.KeyRecMaxMB); err == nil {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= recMaxMB {
			rs.MaxMB = n
		}
	}
	return rs
}

// termRec is the recording of one open terminal; a nil *termRec records
// nothing, so the terminal handler calls it unconditionally.
type termRec struct {
	s    *Server
	id   int64
	file string
	rec  *recording.Recorder
}

// startRecording begins recording a terminal session when recording is on.
// Failures are logged and leave the session unrecorded.
func (s *Server) startRecording(a store.SSHAccess, target, ip string, cols, rows int) *termRec {
	dir := s.recDir()
	if dir == "" || s.secrets == nil {
		return nil
	}
	ctx := context.Background()
	rs := s.loadRecSettings(ctx)
	if !rs.Enabled {
		return nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		s.log.Error("recording: mkdir", "err", err)
		return nil
	}
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	fileID := hex.EncodeToString(b)
	now := s.now()
	rec, err := recording.Create(recording.Options{
		Path: s.recPath(fileID), ID: fileID, Box: s.secrets, Cols: cols, Rows: rows,
		Title: target, MaxBytes: int64(rs.MaxMB) << 20,
	})
	if err != nil {
		s.log.Error("recording: create", "err", err)
		return nil
	}
	id, err := s.store.CreateRecording(ctx, store.Recording{
		FileID: fileID, ServerID: a.ServerID, ServerName: a.Name, Target: target, IP: ip,
		Cols: cols, Rows: rows, StartedAt: now,
	})
	if err != nil {
		s.log.Error("recording: store", "err", err)
		rec.Close()
		os.Remove(s.recPath(fileID))
		return nil
	}
	return &termRec{s: s, id: id, file: fileID, rec: rec}
}

func (t *termRec) Output(p []byte) {
	if t != nil {
		t.rec.Output(p)
	}
}

func (t *termRec) Resize(cols, rows int) {
	if t != nil {
		t.rec.Resize(cols, rows)
	}
}

// finish closes the file and stores its size.
func (t *termRec) finish() {
	if t == nil {
		return
	}
	st, err := t.rec.Close()
	if err != nil {
		t.s.log.Error("recording: write", "id", t.id, "err", err)
	}
	if err := t.s.store.FinishRecording(context.Background(), t.id, t.s.now(), st.Bytes, st.Truncated); err != nil {
		t.s.log.Error("recording: finish", "id", t.id, "err", err)
	}
}

// discard drops a recording of a session that never started.
func (t *termRec) discard() {
	if t == nil {
		return
	}
	t.rec.Close()
	os.Remove(t.s.recPath(t.file))
	_ = t.s.store.DeleteRecording(context.Background(), t.id)
}

// runRecordingPurge ends recordings a restart left open, then deletes
// recordings past retention and stray files, hourly.
func (s *Server) runRecordingPurge(ctx context.Context) {
	if s.recDir() == "" {
		return
	}
	if err := s.store.CloseStaleRecordings(ctx, s.now()); err != nil {
		s.log.Error("recording: close stale", "err", err)
	}
	t := time.NewTicker(recPurgeEvery)
	defer t.Stop()
	for {
		s.purgeRecordings(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (s *Server) purgeRecordings(ctx context.Context) {
	rs := s.loadRecSettings(ctx)
	old, err := s.store.FinishedRecordingsBefore(ctx, s.now().AddDate(0, 0, -rs.RetentionDays))
	if err != nil {
		s.log.Error("recording: purge", "err", err)
		return
	}
	for _, r := range old {
		s.removeRecording(ctx, r)
	}
	// Files without a row: a crash between creating the file and the row,
	// or a row deleted while its session was still writing.
	ids, err := s.store.RecordingFileIDs(ctx)
	if err != nil {
		return
	}
	entries, _ := os.ReadDir(s.recDir())
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".rec")
		if !ok || ids[name] {
			continue
		}
		if fi, err := e.Info(); err == nil && s.now().Sub(fi.ModTime()) > recOrphanAge {
			os.Remove(filepath.Join(s.recDir(), e.Name()))
		}
	}
}

func (s *Server) removeRecording(ctx context.Context, r store.Recording) error {
	if err := os.Remove(s.recPath(r.FileID)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return s.store.DeleteRecording(ctx, r.ID)
}

func (s *Server) recordingRoutes(mux *http.ServeMux) {
	h := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, s.requireSession(f)) }
	h("GET /api/recordings/settings", s.handleRecSettings)
	h("PUT /api/recordings/settings", s.handleRecSaveSettings)
	h("GET /api/recordings", s.handleRecordings)
	h("GET /api/recordings/servers", s.handleRecordingServers)
	h("DELETE /api/recordings", s.handleDeleteAllRecordings)
	h("GET /api/recordings/{id}", s.handleRecording)
	h("GET /api/recordings/{id}/cast", s.handleRecordingCast)
	h("DELETE /api/recordings/{id}", s.handleDeleteRecording)
}

func (s *Server) recSettingsView(ctx context.Context) recSettingsView {
	v := recSettingsView{recSettings: s.loadRecSettings(ctx), Available: s.recDir() != "" && s.secrets != nil}
	if ids, err := s.store.RecordingFileIDs(ctx); err == nil {
		v.Count = len(ids)
	}
	if dir := s.recDir(); dir != "" {
		entries, _ := os.ReadDir(dir)
		for _, e := range entries {
			if fi, err := e.Info(); err == nil && !fi.IsDir() {
				v.DiskBytes += fi.Size()
			}
		}
	}
	return v
}

func (s *Server) handleRecSettings(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.recSettingsView(r.Context()))
}

func (s *Server) handleRecSaveSettings(w http.ResponseWriter, r *http.Request) {
	var req recSettings
	if err := readJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json")
		return
	}
	if req.RetentionDays < 1 || req.RetentionDays > recMaxRetention || req.MaxMB < 1 || req.MaxMB > recMaxMB {
		writeError(w, http.StatusBadRequest, "bad_value")
		return
	}
	enabled := "1"
	if !req.Enabled {
		enabled = "0"
	}
	ctx := r.Context()
	for k, v := range map[string]string{
		store.KeyRecEnabled:   enabled,
		store.KeyRecRetention: strconv.Itoa(req.RetentionDays),
		store.KeyRecMaxMB:     strconv.Itoa(req.MaxMB),
	} {
		if err := s.store.SetSetting(ctx, k, v); err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
	}
	_ = s.store.Audit(ctx, s.clientIP(r), "recording_settings",
		fmt.Sprintf("enabled=%s retention=%dd max=%dMB", enabled, req.RetentionDays, req.MaxMB))
	// A shorter retention applies now, not at the next hourly pass.
	if s.recDir() != "" {
		s.purgeRecordings(ctx)
	}
	writeJSON(w, http.StatusOK, s.recSettingsView(ctx))
}

// handleRecordings lists recordings newest first, a page at a time:
// ?server=<id> filters, ?before=<id> continues after the last row shown.
func (s *Server) handleRecordings(w http.ResponseWriter, r *http.Request) {
	var server, before int64
	for k, dst := range map[string]*int64{"server": &server, "before": &before} {
		if v := r.URL.Query().Get(k); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				writeError(w, http.StatusBadRequest, "bad_id")
				return
			}
			*dst = n
		}
	}
	list, err := s.store.ListRecordings(r.Context(), server, before, recPageSize)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) handleRecordingServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.RecordingServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) recordingByPath(w http.ResponseWriter, r *http.Request) (store.Recording, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "bad_id")
		return store.Recording{}, false
	}
	rec, err := s.store.GetRecording(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "not_found")
		return rec, false
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return rec, false
	}
	return rec, true
}

func (s *Server) handleRecording(w http.ResponseWriter, r *http.Request) {
	if rec, ok := s.recordingByPath(w, r); ok {
		writeJSON(w, http.StatusOK, rec)
	}
}

// handleRecordingCast streams the decrypted asciicast for the player, or as
// a .cast file with ?download=1.
func (s *Server) handleRecordingCast(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.recordingByPath(w, r)
	if !ok {
		return
	}
	if s.recDir() == "" {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	rc, err := recording.Open(s.recPath(rec.FileID), rec.FileID, s.secrets)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusNotFound, "no_file")
		return
	} else if err != nil {
		s.log.Error("recording: open", "id", rec.ID, "err", err)
		writeError(w, http.StatusInternalServerError, "unreadable")
		return
	}
	defer rc.Close()
	download := r.URL.Query().Get("download") == "1"
	event := "recording_view"
	if download {
		event = "recording_download"
		name := fmt.Sprintf("srvmon-%s-%s.cast", safeFileName(rec.ServerName), rec.StartedAt.Format("20060102-150405"))
		w.Header().Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(name))
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), event, fmt.Sprintf("#%d %s", rec.ID, rec.Target))
	w.Header().Set("Content-Type", "application/x-asciicast")
	w.Header().Set("Cache-Control", "no-store")
	if _, err := io.Copy(w, rc); err != nil {
		s.log.Warn("recording: stream", "id", rec.ID, "err", err)
	}
}

func safeFileName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			return r
		}
		return '_'
	}, s)
}

func (s *Server) handleDeleteRecording(w http.ResponseWriter, r *http.Request) {
	rec, ok := s.recordingByPath(w, r)
	if !ok {
		return
	}
	if err := s.removeRecording(r.Context(), rec); err != nil {
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	_ = s.store.Audit(r.Context(), s.clientIP(r), "recording_delete", fmt.Sprintf("#%d %s", rec.ID, rec.Target))
	writeJSON(w, http.StatusOK, map[string]bool{"deleted": true})
}

func (s *Server) handleDeleteAllRecordings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	n := 0
	for {
		list, err := s.store.ListRecordings(ctx, 0, 0, recListLimit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal")
			return
		}
		for _, rec := range list {
			if err := s.removeRecording(ctx, rec); err != nil {
				writeError(w, http.StatusInternalServerError, "internal")
				return
			}
			n++
		}
		if len(list) < recListLimit {
			break
		}
	}
	_ = s.store.Audit(ctx, s.clientIP(r), "recording_delete_all", strconv.Itoa(n))
	writeJSON(w, http.StatusOK, map[string]int{"deleted": n})
}
