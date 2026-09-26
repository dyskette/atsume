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
			// Not the same as a failed download, and not fixed the same way:
			// the check worked, the site listed nothing.
			name:  "checked and found nothing is its own state",
			row:   LibraryRow{Series: store.Series{CheckedAt: checked}},
			label: "no chapters found", kind: "empty",
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
		Series: store.Series{
			ModuleName: "Madara", ModuleKey: "Madara",
			Status: "ongoing", Subscribed: true,
			CheckedAt: sql.NullTime{Time: time.Now(), Valid: true},
		},
		Progress: store.SeriesProgress{Total: 40, Done: 12},
	}
	// The site is rendered separately so it can be a link to its settings,
	// which is where a failing row is usually fixed.
	if got, want := r.Site(), "Madara"; got != want {
		t.Errorf("site = %q, want %q", got, want)
	}
	if got, want := r.Detail(), " · ongoing · 12 of 40 downloaded · checked just now"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	// When it was last looked at is on every row: without it the page looks
	// the same whether checking works or stopped weeks ago.
	r.Series.CheckedAt = sql.NullTime{}
	if got := r.Detail(); !strings.Contains(got, "never checked") {
		t.Errorf("got %q, want it to say it has never been checked", got)
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
	if len(got) != 4 {
		t.Fatalf("got %d sections, want 4", len(got))
	}
	// Order is the order of the reader's questions: what produced nothing,
	// what broke, what is new, then the collection. The first two used to
	// share a heading, which offered one answer to two different problems.
	if got[0].Title != "Nothing found on the site" || len(got[0].Rows) != 1 {
		t.Errorf("first section = %q with %d rows", got[0].Title, len(got[0].Rows))
	}
	if got[0].Rows[0].Series.Title != "Empty" {
		t.Errorf("first section holds %q", got[0].Rows[0].Series.Title)
	}
	if got[1].Title != "Downloads failed" || len(got[1].Rows) != 1 {
		t.Errorf("second section = %q with %d rows", got[1].Title, len(got[1].Rows))
	}
	if got[1].Rows[0].Series.Title != "Broken" {
		t.Errorf("second section holds %q", got[1].Rows[0].Series.Title)
	}
	if got[2].Title != "New chapters" || len(got[2].Rows) != 1 {
		t.Errorf("third section = %q with %d rows", got[2].Title, len(got[2].Rows))
	}
	if got[2].Rows[0].Series.Title != "Arrived" {
		t.Errorf("new section holds %q", got[2].Rows[0].Series.Title)
	}
	// A backlog is not news, however large it is.
	if got[3].Title != "Everything else" || len(got[3].Rows) != 2 {
		t.Errorf("last section = %q with %d rows", got[3].Title, len(got[3].Rows))
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
	read := time.Now().Add(-time.Hour)
	catalogues := map[string]store.SiteCatalogue{
		"Alpha": {Exists: true, Complete: true, Titles: 10, Source: store.SourceSite, BuiltAt: read, Resume: store.NoPos},
		"Beta":  {Exists: true, Titles: 3, Problem: app.ProblemBlocked, Status: 403, BuiltAt: read, Resume: store.ReadPos{Dir: 0, Page: 2}},
		"Delta": {Exists: true, Complete: true, Titles: 40, Source: store.SourcePrebuilt, DataAt: read, Resume: store.NoPos},
	}
	usage := map[string]store.SiteUse{
		"Alpha": {Series: 3, Following: 2, NewToday: 2},
		"Beta":  {Series: 1, Following: 1, CheckError: "network problem"},
	}
	build := func(in SitesInput) SitesView {
		in.Catalogue, in.Catalogues, in.Usage = cat, catalogues, usage
		return BuildSites(in)
	}

	v := build(SitesInput{})
	if v.Total != 5 || v.Found != 5 || v.Counts() != "5 available · 2 you use" {
		t.Fatalf("total=%d found=%d counts=%q", v.Total, v.Found, v.Counts())
	}
	status := map[string]string{}
	for _, r := range v.Rows {
		status[r.Entry.Site] = r.Status
	}
	want := map[string]string{"Alpha": "Working", "Beta": "Blocked", "Gamma": "Not read yet", "Delta": "Shared list", "Epsilon": "Not read yet"}
	for site, w := range want {
		if status[site] != w {
			t.Errorf("%s: status %q, want %q", site, status[site], w)
		}
	}
	// Largest category first; whatever declares none goes last, named.
	if v.Chips[0].Name != "English" || v.Chips[len(v.Chips)-1].Name != uncategorised {
		t.Errorf("chips = %+v", v.Chips)
	}
	// The cards say the one thing worth knowing: a failing check before
	// anything else, then what arrived today.
	lines := map[string]string{}
	for _, c := range v.Yours {
		lines[c.Site] = c.Line
	}
	if lines["Alpha"] != "2 new chapters today" || lines["Beta"] != "Chapter checks failing" {
		t.Errorf("card lines = %v", lines)
	}

	if got := build(SitesInput{Query: "ta"}).Found; got != 2 {
		t.Errorf("name search found %d, want Beta and Delta", got)
	}
	if got := build(SitesInput{Category: "Raw"}).Found; got != 1 {
		t.Errorf("category filter found %d, want 1", got)
	}
	if got := build(SitesInput{HideProblems: true}).Found; got != 4 {
		t.Errorf("hiding problems left %d, want everything but Beta", got)
	}
}

// TestStalled covers the warning that a library cannot do without.
//
// A scheduler that has died looks exactly like one with nothing due: every
// row keeps its last known state and the page goes on implying it is current.
func TestStalled(t *testing.T) {
	rows := []LibraryRow{{Series: store.Series{Title: "Anything"}}}
	const interval = time.Hour

	cases := []struct {
		name string
		v    LibraryView
		want bool
	}{
		{
			name: "a recent sweep is fine",
			v: LibraryView{Rows: rows, CheckInterval: interval,
				Swept: true, LastSweep: time.Now().Add(-30 * time.Minute)},
		},
		{
			// One late sweep is not an alarm; the queue can be busy.
			name: "a sweep running late is not an alarm",
			v: LibraryView{Rows: rows, CheckInterval: interval,
				Swept: true, LastSweep: time.Now().Add(-90 * time.Minute)},
		},
		{
			name: "past the grace, say so",
			v: LibraryView{Rows: rows, CheckInterval: interval,
				Swept: true, LastSweep: time.Now().Add(-5 * time.Hour)},
			want: true,
		},
		{
			// The first sweep is deliberately delayed, so a fresh start is
			// not evidence of anything.
			name: "just started and nothing swept yet",
			v:    LibraryView{Rows: rows, CheckInterval: interval, Uptime: time.Minute},
		},
		{
			name: "up for hours and never swept",
			v:    LibraryView{Rows: rows, CheckInterval: interval, Uptime: 5 * time.Hour},
			want: true,
		},
		{
			// Switched off on purpose is not a fault.
			name: "checking is disabled",
			v:    LibraryView{Rows: rows, Uptime: 5 * time.Hour},
		},
		{
			name: "an empty library has nothing to be stale",
			v:    LibraryView{CheckInterval: interval, Uptime: 5 * time.Hour},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, why := c.v.Stalled()
			if got != c.want {
				t.Errorf("Stalled() = %v, want %v (%q)", got, c.want, why)
			}
			if got && why == "" {
				t.Error("a warning with nothing to say is no warning")
			}
		})
	}
}

// TestLibraryWaiting covers what the page can act on.
func TestLibraryWaiting(t *testing.T) {
	v := LibraryView{Rows: []LibraryRow{
		{Progress: store.SeriesProgress{Total: 4, Done: 4}},
		{Progress: store.SeriesProgress{Total: 4, Done: 1, Waiting: 3}},
		{Progress: store.SeriesProgress{Total: 2, Waiting: 2}},
		// Already running: nothing to queue, so nothing to offer.
		{Progress: store.SeriesProgress{Total: 5, Active: 5}},
	}}
	series, chapters := v.Waiting()
	if series != 2 || chapters != 5 {
		t.Errorf("waiting = %d series / %d chapters, want 2 / 5", series, chapters)
	}
	if v.Rows[0].Actionable() || v.Rows[3].Actionable() {
		t.Error("a row with nothing to fetch offers nothing")
	}
	if !v.Rows[1].Actionable() || v.Rows[1].Waiting() != 3 {
		t.Error("a row with a backlog offers to fetch it")
	}
}

// TestEscapeQueryValue covers the readability of the addresses a reader sees.
//
// RFC 3986 allows "/" and ":" in a query, and escaping them turns
// ?url=/title/da0ccc81 into ?url=%2Ftitle%2Fda0ccc81 for no benefit. What
// genuinely has to be escaped still is, or a link with an ampersand in it
// would split into two parameters.
func TestEscapeQueryValue(t *testing.T) {
	cases := []struct{ in, want string }{
		{"/title/da0ccc81-68ef-4b0b-8023-52f60046d714", "/title/da0ccc81-68ef-4b0b-8023-52f60046d714"},
		{"https://mangatoon.mobi/en/x", "https://mangatoon.mobi/en/x"},
		// These would otherwise change what the query means.
		{"/a?b=1&c=2", "/a%3Fb%3D1%26c%3D2"},
		{"/a#b", "/a%23b"},
		{"/a b", "/a+b"},
		{"/100%", "/100%25"},
	}
	for _, c := range cases {
		if got := escapeQueryValue(c.in); got != c.want {
			t.Errorf("escapeQueryValue(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
