package web

import (
	"log/slog"
	"net/http"

	"github.com/a-h/templ"
)

// render writes a component, setting the content type first.
func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := c.Render(r.Context(), w); err != nil {
		// The status is already sent by this point, so there is nothing to do
		// but record it.
		slog.Error("render", "path", r.URL.Path, "err", err)
	}
}

// fail reports an error to the operator and the browser.
//
// Scraping errors are expected and routine — a site changed, a module broke —
// so they are surfaced in the response rather than hidden behind a generic 500.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "err", err)
	http.Error(w, err.Error(), http.StatusInternalServerError)
}
