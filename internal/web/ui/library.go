package ui

import (
	"fmt"

	"github.com/dyskette/atsume/internal/store"
)

// LibraryRow is one series as the library lists it.
type LibraryRow struct {
	Series   store.Series
	Progress store.SeriesProgress
}

// LibraryView is the library page.
type LibraryView struct {
	Rows []LibraryRow
	// Destination is where chapters are written, stated on the empty page
	// because it is the only thing connecting atsume to a library server.
	Destination string
}

// State is the short answer to "is this one up to date", which is the question
// the library exists to answer. It is deliberately about the manga rather than
// about when the program last ran.
func (r LibraryRow) State() (label, kind string) {
	switch {
	case r.Progress.Active > 0:
		return fmt.Sprintf("%d downloading", r.Progress.Active), "downloading"
	case r.Progress.Failed > 0:
		return fmt.Sprintf("%d failed", r.Progress.Failed), "failed"
	case r.Progress.Total == 0 && !r.Series.CheckedAt.Valid:
		return "not checked yet", ""
	case r.Progress.Total == 0:
		return "no chapters found", "failed"
	case r.Progress.Waiting > 0:
		return fmt.Sprintf("%d not downloaded", r.Progress.Waiting), "waiting"
	default:
		return "up to date", "done"
	}
}

// Detail is the supporting line: where it came from and how much is on disk.
func (r LibraryRow) Detail() string {
	out := r.Series.ModuleName
	if r.Series.Status != "" {
		out += " · " + r.Series.Status
	}
	if r.Progress.Total > 0 {
		out += fmt.Sprintf(" · %d of %d downloaded", r.Progress.Done, r.Progress.Total)
	}
	if !r.Series.Subscribed {
		out += " · not following"
	}
	return out
}

// NeedsAttention reports whether anything on the page is wrong, so the page can
// say so once at the top rather than making the reader scan every row.
func (v LibraryView) NeedsAttention() int {
	var n int
	for _, r := range v.Rows {
		if _, kind := r.State(); kind == "failed" {
			n++
		}
	}
	return n
}
