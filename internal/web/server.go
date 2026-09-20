// Package web serves the user interface.
package web

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/dyskette/atsume/internal/app"
)

// Server is the HTTP front end.
type Server struct {
	App  *app.App
	http *http.Server
}

// New builds a server listening on addr.
func New(a *app.App, addr string) *Server {
	s := &Server{App: a}
	s.http = &http.Server{
		Addr:    addr,
		Handler: s.routes(),
		// No WriteTimeout: the SSE stream is a long-lived response and any
		// deadline here would cut it off mid-download.
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	return s
}

// ListenAndServe starts the server.
func (s *Server) ListenAndServe() error {
	slog.Info("listening", "addr", s.http.Addr)
	return s.http.ListenAndServe()
}

// Shutdown stops the server gracefully.
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
