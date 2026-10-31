package api

import (
	"compress/gzip"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/askidmobile/srvmon/internal/auth"
	"github.com/askidmobile/srvmon/internal/metrics"
	"github.com/askidmobile/srvmon/internal/protocol"
	"github.com/askidmobile/srvmon/internal/store"
)

const maxIngestBody = 4 << 20 // after decompression

func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "token_required")
		return
	}
	serverID, name, err := s.store.AgentServer(r.Context(), auth.HashToken(token))
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusUnauthorized, "invalid_token")
		return
	} else if err != nil {
		s.log.Error("agent lookup", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}

	var body io.Reader = http.MaxBytesReader(w, r.Body, maxIngestBody)
	if r.Header.Get("Content-Encoding") == "gzip" {
		zr, err := gzip.NewReader(body)
		if err != nil {
			writeError(w, http.StatusBadRequest, "bad_gzip")
			return
		}
		defer zr.Close()
		body = io.LimitReader(zr, maxIngestBody)
	}
	var reports []protocol.Report
	if err := json.NewDecoder(body).Decode(&reports); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json")
		return
	}
	if err := s.vm.Write(r.Context(), name, reports...); err != nil {
		// Tell the agent to retry: it keeps the batch in its buffer.
		s.log.Warn("victoriametrics write", "server", name, "err", err)
		writeError(w, http.StatusBadGateway, "storage_unavailable")
		return
	}
	s.latest.Put(serverID, reports...)
	writeJSON(w, http.StatusOK, protocol.IngestResponse{Detail: s.latest.Watching(serverID)})
}

type serverView struct {
	store.Server
	Online   bool              `json:"online"`
	Snapshot *metrics.Snapshot `json:"snapshot,omitempty"`
}

func (s *Server) handleServers(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListServers(r.Context())
	if err != nil {
		s.log.Error("list servers", "err", err)
		writeError(w, http.StatusInternalServerError, "internal")
		return
	}
	out := make([]serverView, 0, len(list))
	now := s.now()
	for _, srv := range list {
		v := serverView{Server: srv}
		if snap, ok := s.latest.Get(srv.ID); ok {
			v.Snapshot = &snap
			v.Online = now.Sub(snap.Received) < offlineAfter
		}
		out = append(out, v)
	}
	writeJSON(w, http.StatusOK, out)
}
