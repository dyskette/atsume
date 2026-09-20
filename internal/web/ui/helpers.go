// Package ui holds the templ components for the web interface.
package ui

import (
	"fmt"
	"strings"

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
