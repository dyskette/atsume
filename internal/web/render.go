package web

import (
	"log/slog"
	"net/http"
	"strings"

	"github.com/a-h/templ"
	"github.com/dyskette/atsume/internal/web/ui"
)

// render writes a component, setting the content type first.
func (s *Server) render(w http.ResponseWriter, r *http.Request, c templ.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Every page says how things stand now, so a browser going back must not
	// show a copy from earlier: a site's page saved while its first read ran
	// came back reading, and polled for a list that was long finished.
	w.Header().Set("Cache-Control", "no-store")
	if err := c.Render(r.Context(), w); err != nil {
		// The status is already sent by this point, so there is nothing to do
		// but record it.
		slog.Error("render", "path", r.URL.Path, "err", err)
	}
}

// fail reports an error to the operator and the browser.
//
// Scraping errors are expected and routine — a site changed, a module broke, a
// host blocked the request — so they are shown with the context needed to act
// on them. A bare error string on a blank page says nothing about whether the
// site is down, the module is stale, or atsume is at fault.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	slog.Error("request failed", "path", r.URL.Path, "err", err)

	summary, hints := explain(err)

	// An htmx swap replaces a fragment of the page, so it gets the block on its
	// own; a full load gets the block with the usual chrome around it.
	if r.Header.Get("HX-Request") == "true" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = ui.Failure(summary, err.Error(), hints).Render(r.Context(), w)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadGateway)
	_ = ui.FailurePage("Something went wrong", summary, err.Error(), hints).Render(r.Context(), w)
}

// explain turns an error into a summary and whatever is worth checking.
//
// The causes are nearly always external, and which one it is decides what to do
// next, so the guesses are worth making explicitly rather than leaving the
// reader to interpret a Go error string.
func explain(err error) (string, []string) {
	text := strings.ToLower(err.Error())

	switch {
	case strings.Contains(text, "network problem"), strings.Contains(text, "no such host"),
		strings.Contains(text, "connection refused"), strings.Contains(text, "timeout"),
		strings.Contains(text, "deadline exceeded"):
		return "The site did not answer.", []string{
			"It may be down, or blocking requests from this address.",
			"Sites in this catalogue move domains often; the module may be pointing at an address that no longer exists.",
			"If it is behind an anti-bot challenge, set ATSUME_FLARESOLVERR_URL so those can be solved.",
		}
	case strings.Contains(text, "no module named"):
		return "That module is not in the pinned revision.", []string{
			"It may have been added upstream since the revision atsume is pinned to.",
		}
	case strings.Contains(text, "login"), strings.Contains(text, "credentials"):
		return "The module could not sign in.", []string{
			"Check the username and password on the site's settings page.",
			"Storing a login needs ATSUME_SECRET_KEY to be set.",
		}
	case strings.Contains(text, "attempt to") || strings.Contains(text, "lua"):
		return "The module failed while running.", []string{
			"This is a fault in the module or in atsume's support for it, not in the site.",
			"The full message below is what the module reported.",
		}
	default:
		return "The request could not be completed.", nil
	}
}
