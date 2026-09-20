// Package ui holds the templ components for the web interface.
package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// pageURL builds a directory URL for a module page.
func pageURL(module string, page int) string {
	if page <= 0 {
		return "/modules/" + module
	}
	return fmt.Sprintf("/modules/%s?page=%d", module, page)
}

// stateLabel renders a chapter's state for display.
func stateLabel(c store.Chapter) string {
	switch c.State {
	case store.ChapterDone:
		if c.Pages > 0 {
			return fmt.Sprintf("%d pages", c.Pages)
		}
		return "done"
	case store.ChapterDownloading:
		return "downloading"
	case store.ChapterQueued:
		return "queued"
	case store.ChapterFailed:
		return "failed"
	default:
		return "pending"
	}
}

// lastChecked renders when a series was last looked at, which is the only
// signal that automatic checking is actually running.
func lastChecked(s store.Series) string {
	if !s.CheckedAt.Valid {
		return "never checked"
	}
	d := time.Since(s.CheckedAt.Time)
	switch {
	case d < time.Minute:
		return "checked just now"
	case d < time.Hour:
		return fmt.Sprintf("checked %dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("checked %dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("checked %dd ago", int(d.Hours()/24))
	}
}

// optionLabel prefers the caption a module supplied, falling back to its key.
func optionLabel(o scraper.Option) string {
	if o.Caption != "" {
		return o.Caption
	}
	return o.Name
}

// passwordPlaceholder hints whether a password is already stored, without
// revealing anything about it.
func passwordPlaceholder(s *app.ModuleSettings) string {
	if s.HasCredentials {
		return "unchanged"
	}
	return ""
}

// truncate shortens a message for inline display, keeping the full text in the
// element's title attribute.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Progress renders the live progress line swapped in over SSE.
func Progress(done, total int) string {
	if total == 0 {
		return "downloading"
	}
	return fmt.Sprintf("%d/%d pages", done, total)
}
