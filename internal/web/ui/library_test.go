package ui

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// TestLibraryRowState pins the one-line answer each row gives.
//
// "Is this up to date" is the question the library exists to answer; the old
// row reported when the program last ran instead, which nobody asked.
func TestLibraryRowState(t *testing.T) {
	checked := sql.NullTime{Time: time.Now(), Valid: true}

	cases := []struct {
		name  string
		row   LibraryRow
		label string
		kind  string
	}{
		{
			name:  "everything on disk",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 40}},
			label: "up to date", kind: "done",
		},
		{
			// A download finishes on its own; a failure waits for the reader.
			// Reporting the download first hid the row that needed them.
			name:  "a failure outranks work in progress",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 10, Active: 2, Waiting: 28, Failed: 1}},
			label: "1 chapter failed", kind: "failed",
		},
		{
			name:  "work in progress otherwise wins",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 10, Active: 2, Waiting: 28}},
			label: "2 downloading", kind: "downloading",
		},
		{
			name:  "failures outrank merely waiting",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 38, Waiting: 2, Failed: 2}},
			label: "2 chapters failed", kind: "failed",
		},
		{
			// The distinction the whole change exists for: a chapter that came
			// out reads differently from a back catalogue that was always
			// there, even though both are "not downloaded".
			name:  "a new chapter is not a backlog",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 39, Waiting: 1, New: 1}},
			label: "1 new chapter", kind: "new",
		},
		{
			name:  "waiting",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 37, Waiting: 3}},
			label: "3 not downloaded", kind: "waiting",
		},
		{
			name:  "never checked is not a failure",
			row:   LibraryRow{Series: store.Series{}},
			label: "not checked yet", kind: "",
		},
		{
			name:  "checked and found nothing is a failure",
			row:   LibraryRow{Series: store.Series{CheckedAt: checked}},
			label: "no chapters found", kind: "failed",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, kind := c.row.State()
			if label != c.label || kind != c.kind {
				t.Errorf("got (%q, %q), want (%q, %q)", label, kind, c.label, c.kind)
			}
		})
	}
}

func TestLibraryRowDetail(t *testing.T) {
	r := LibraryRow{
		Series:   store.Series{ModuleName: "Madara", Status: "ongoing", Subscribed: true},
		Progress: store.SeriesProgress{Total: 40, Done: 12},
	}
	if got, want := r.Detail(), "Madara · ongoing · 12 of 40 downloaded"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// Not following is worth saying: the row would otherwise look the same as
	// one that is being kept current.
	r.Series.Subscribed = false
	if got := r.Detail(); !strings.Contains(got, "not following") {
		t.Errorf("got %q, want it to mention not following", got)
	}
}

// TestSections covers the grouping that lets the page answer "is there
// anything new" without the reader scanning every row for it.
func TestSections(t *testing.T) {
	checked := sql.NullTime{Time: time.Now(), Valid: true}
	rows := []LibraryRow{
		{Series: store.Series{Title: "Calm"}, Progress: store.SeriesProgress{Total: 1, Done: 1}},
		{Series: store.Series{Title: "Broken"}, Progress: store.SeriesProgress{Total: 2, Done: 1, Failed: 1, Waiting: 1}},
		{Series: store.Series{Title: "Empty", CheckedAt: checked}},
		{Series: store.Series{Title: "Arrived"}, Progress: store.SeriesProgress{Total: 3, Done: 2, Waiting: 1, New: 1}},
		{Series: store.Series{Title: "Backlog"}, Progress: store.SeriesProgress{Total: 30, Waiting: 30}},
	}

	got := LibraryView{Rows: rows}.Sections()
	if len(got) != 3 {
		t.Fatalf("got %d sections, want 3", len(got))
	}
	// Order is the order of the reader's questions: what is wrong, what is
	// new, then the collection.
	if got[0].Title != "Needs attention" || len(got[0].Rows) != 2 {
		t.Errorf("first section = %q with %d rows", got[0].Title, len(got[0].Rows))
	}
	if got[1].Title != "New chapters" || len(got[1].Rows) != 1 {
		t.Errorf("second section = %q with %d rows", got[1].Title, len(got[1].Rows))
	}
	if got[1].Rows[0].Series.Title != "Arrived" {
		t.Errorf("new section holds %q", got[1].Rows[0].Series.Title)
	}
	// A backlog is not news, however large it is.
	if got[2].Title != "Everything else" || len(got[2].Rows) != 2 {
		t.Errorf("third section = %q with %d rows", got[2].Title, len(got[2].Rows))
	}

	// Rows arrive alphabetically and must stay that way inside a section, so
	// a series is where the reader last saw it.
	if got[0].Rows[0].Series.Title != "Broken" || got[0].Rows[1].Series.Title != "Empty" {
		t.Errorf("attention section reordered: %q, %q",
			got[0].Rows[0].Series.Title, got[0].Rows[1].Series.Title)
	}

	// With nothing wrong and nothing new there is one group, and calling it
	// "Everything else" would be answering a question nobody asked.
	only := LibraryView{Rows: rows[:1]}.Sections()
	if len(only) != 1 || only[0].Title != "Library" {
		t.Errorf("single section = %+v", only)
	}
}

// TestDetailShowsArrival covers the evidence for a "new" claim.
func TestDetailShowsArrival(t *testing.T) {
	r := LibraryRow{
		Series: store.Series{ModuleName: "Madara", Subscribed: true},
		Progress: store.SeriesProgress{
			Total: 3, Done: 2, Waiting: 1, New: 1,
			NewestArrival: sql.NullTime{Time: time.Now().Add(-3 * time.Hour), Valid: true},
		},
	}
	if got := r.Detail(); !strings.Contains(got, "arrived 3h ago") {
		t.Errorf("got %q, want it to say when the chapter arrived", got)
	}

	// A backlog has no arrival worth reporting: it was there all along.
	r.Progress.New = 0
	if got := r.Detail(); strings.Contains(got, "arrived") {
		t.Errorf("got %q, want no arrival for a backlog", got)
	}
}

// TestBuildSites covers the grouping that makes several hundred sites usable.
func TestBuildSites(t *testing.T) {
	cat := app.Catalogue{Ref: "master", Entries: []app.ModuleEntry{
		{Site: "Alpha", Category: "English"},
		{Site: "Beta", Category: "English"},
		{Site: "Gamma", Category: "English"},
		{Site: "Delta", Category: "Raw"},
		{Site: "Epsilon"},
	}}

	v := BuildSites(cat, "")
	if v.Total != 5 || v.Found != 5 {
		t.Fatalf("total=%d found=%d", v.Total, v.Found)
	}
	// Largest category first; alphabetical order would bury the useful ones.
	if v.Groups[0].Category != "English" || len(v.Groups[0].Entries) != 3 {
		t.Errorf("first group = %+v", v.Groups[0])
	}
	// Whatever declares no category goes last, named rather than hidden.
	last := v.Groups[len(v.Groups)-1]
	if last.Category != uncategorised || len(last.Entries) != 1 {
		t.Errorf("last group = %+v", last)
	}

	// A search flattens the result: grouping a handful of matches only makes
	// them harder to scan.
	v = BuildSites(cat, "ta")
	if v.Found != 2 {
		t.Fatalf("found = %d, want Beta and Delta", v.Found)
	}
	if len(v.Groups) != 1 || v.Groups[0].Category != "" {
		t.Errorf("search results should be one unnamed group, got %+v", v.Groups)
	}

	// Category is searchable too.
	if got := BuildSites(cat, "raw").Found; got != 1 {
		t.Errorf("category search found %d, want 1", got)
	}
	if got := BuildSites(cat, "nothing here").Found; got != 0 {
		t.Errorf("no-match search found %d", got)
	}
}
