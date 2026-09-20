package web

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/dyskette/atsume/internal/web/static"
)

// routes declares the whole URL surface in one place.
func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", s.handleLibrary)
	mux.HandleFunc("GET /modules", s.handleModules)
	mux.HandleFunc("GET /modules/{name}", s.handleBrowse)
	mux.HandleFunc("GET /modules/{name}/settings", s.handleModuleSettings)
	mux.HandleFunc("POST /modules/{name}/settings", s.handleSaveModuleSettings)

	mux.HandleFunc("POST /series", s.handleTrackSeries)
	mux.HandleFunc("GET /series/{id}", s.handleSeries)
	mux.HandleFunc("POST /series/{id}/refresh", s.handleRefreshSeries)
	mux.HandleFunc("POST /series/{id}/download", s.handleDownloadSeries)

	mux.HandleFunc("POST /series/{id}/subscribe", s.handleSubscribe)
	mux.HandleFunc("POST /check", s.handleCheckNow)

	mux.HandleFunc("POST /chapters/{id}/download", s.handleDownloadChapter)

	mux.HandleFunc("GET /events", s.handleEvents)
	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.Handle("GET /static/", http.StripPrefix("/static/",
		http.FileServer(http.FS(static.FS))))

	return logRequests(mux)
}

// logRequests records each request once it completes.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		// The event stream stays open for the life of the page; logging its
		// duration on close would be noise, so it is logged at debug only.
		level := slog.LevelInfo
		if r.URL.Path == "/events" || r.URL.Path == "/healthz" {
			level = slog.LevelDebug
		}
		slog.Log(r.Context(), level, "request",
			"method", r.Method, "path", r.URL.Path,
			"status", rec.status, "took", time.Since(start))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush forwards to the underlying writer so that SSE keeps streaming through
// the logging wrapper.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
