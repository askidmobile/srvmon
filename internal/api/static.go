package api

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// offlineAfter marks a server offline when no push arrived for this long.
const offlineAfter = 2 * time.Minute

// staticHandler serves the built SPA and falls back to index.html for
// client-side routes.
func (s *Server) staticHandler() http.Handler {
	if s.web == nil {
		return http.HandlerFunc(notBuilt)
	}
	if _, err := fs.Stat(s.web, "index.html"); err != nil {
		return http.HandlerFunc(notBuilt)
	}
	files := http.FileServerFS(s.web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p != "" {
			if st, err := fs.Stat(s.web, p); err == nil && !st.IsDir() {
				if strings.HasPrefix(p, "assets/") {
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-cache")
		http.ServeFileFS(w, r, s.web, "index.html")
	})
}

func notBuilt(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("srvmon: frontend is not built. Run `make web` or use the Docker image.\n"))
}
