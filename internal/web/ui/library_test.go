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
			name:  "work in progress wins over everything",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 10, Active: 2, Waiting: 28, Failed: 1}},
			label: "2 downloading", kind: "downloading",
		},
		{
			name:  "failures outrank merely waiting",
			row:   LibraryRow{Progress: store.SeriesProgress{Total: 40, Done: 38, Waiting: 2, Failed: 2}},
			label: "2 failed", kind: "failed",
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

func TestNeedsAttention(t *testing.T) {
	v := LibraryView{Rows: []LibraryRow{
		{Progress: store.SeriesProgress{Total: 1, Done: 1}},
		{Progress: store.SeriesProgress{Total: 2, Done: 1, Failed: 1, Waiting: 1}},
		{Series: store.Series{CheckedAt: sql.NullTime{Time: time.Now(), Valid: true}}},
	}}
	if got := v.NeedsAttention(); got != 2 {
		t.Errorf("NeedsAttention() = %d, want 2", got)
	}
}

// TestBuildSites covers the grouping that makes several hundred sites usable.
func TestBuildSites(t *testing.T) {
	cat := app.Catalogue{Ref: "master", Entries: []app.ModuleEntry{
		{Name: "Alpha", Category: "English"},
		{Name: "Beta", Category: "English"},
		{Name: "Gamma", Category: "English"},
		{Name: "Delta", Category: "Raw"},
		{Name: "Epsilon"},
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
