package ui

import (
	"fmt"
	"time"

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
	case r.Progress.Failed > 0:
		return Count(r.Progress.Failed, "chapter failed", "chapters failed"), "failed"
	case r.Progress.Active > 0:
		return fmt.Sprintf("%d downloading", r.Progress.Active), "downloading"
	case r.Progress.Total == 0 && !r.Series.CheckedAt.Valid:
		return "not checked yet", ""
	// Distinct from a failed download: the check worked and the site listed
	// nothing. Retrying the download would be the wrong move, and grouping
	// the two under one heading offered one answer to two questions.
	case r.Progress.Total == 0:
		return "no chapters found", "empty"
	// A chapter that came out is not the same event as a back catalogue that
	// was always there, and saying "24 not downloaded" for both is what made
	// the page unable to answer the only question it is opened for.
	case r.Progress.New > 0:
		return Count(r.Progress.New, "new chapter", "new chapters"), "new"
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
	// Recency belongs on the rows that claim to be new, and nowhere else: it
	// is the evidence for the claim.
	if r.Progress.New > 0 && r.Progress.NewestArrival.Valid {
		out += " · arrived " + ago(r.Progress.NewestArrival.Time)
	}
	if !r.Series.Subscribed {
		out += " · not following"
	}
	return out
}

// Section is one group of library rows under a heading that says what the
// group means.
type Section struct {
	Title string
	Blurb string
	Rows  []LibraryRow
}

// Sections splits the library into what is wrong, what is new, and everything
// else.
//
// One alphabetical list could not answer "is there anything new", because the
// answer was scattered through it by title. Grouping puts the answer at the
// top; keeping each group alphabetical means a series a reader goes looking
// for is still where they left it, which sorting the whole page by recency
// would have cost.
func (v LibraryView) Sections() []Section {
	var empty, failed, arrived, rest []LibraryRow
	for _, r := range v.Rows {
		_, kind := r.State()
		switch {
		case kind == "empty":
			empty = append(empty, r)
		case kind == "failed":
			failed = append(failed, r)
		case r.Progress.New > 0:
			arrived = append(arrived, r)
		default:
			rest = append(rest, r)
		}
	}

	out := make([]Section, 0, 4)
	// Two different failures with two different fixes. Under one heading a
	// reader had to open each row to find out which kind it was.
	if len(empty) > 0 {
		out = append(out, Section{
			Title: "Nothing found on the site",
			Blurb: "The check worked and the site listed no chapters. Usually a " +
				"login it wants, or an address that has moved.",
			Rows: empty,
		})
	}
	if len(failed) > 0 {
		out = append(out, Section{
			Title: "Downloads failed",
			Blurb: "The chapters are known; fetching them did not work. These can " +
				"be retried.",
			Rows: failed,
		})
	}
	if len(arrived) > 0 {
		out = append(out, Section{
			Title: "New chapters",
			Blurb: "Published since you followed the series.",
			Rows:  arrived,
		})
	}
	if len(rest) > 0 {
		title := "Everything else"
		if len(out) == 0 {
			title = "Library"
		}
		out = append(out, Section{Title: title, Rows: rest})
	}
	return out
}

// ago is a rough relative time. The library needs "recently or not", not a
// timestamp a reader has to subtract from today's date.
func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}
