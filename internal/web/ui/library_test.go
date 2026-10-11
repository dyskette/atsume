package ui

import (
	"bytes"
	"context"
	"database/sql"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// TestLibraryPill pins the one-line answer each row gives, and the order in
// which a row's states outrank each other: what needs the reader first, then
// what is moving, then what is merely true.
func TestLibraryPill(t *testing.T) {
	now := sql.NullTime{Time: time.Now(), Valid: true}
	following := store.Series{Subscribed: true, CheckedAt: now}
	failing := following
	failing.CheckProblem, failing.CheckFailedAt = app.ProblemBlocked, now
	unknown := following
	unknown.CheckFailedAt = now
	ended := following
	ended.Status = "Completed"

	cases := []struct {
		name        string
		row         LibraryRow
		label, tone string
	}{
		{"everything on disk", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 40}}, "Up to date", "ok"},
		{"a failing check names its kind", LibraryRow{Series: failing, Progress: store.SeriesProgress{Total: 40, Done: 39, Failed: 1, Waiting: 1}}, "Site blocked", "bad"},
		{"a failure of no known kind", LibraryRow{Series: unknown, Progress: store.SeriesProgress{Total: 4, Done: 4}}, "Check failing", "bad"},
		{"a failed download outranks one running", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 10, Active: 2, Downloading: 1, Waiting: 28, Failed: 2}}, "2 failed", "bad"},
		{"downloading", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 10, Active: 2, Downloading: 1}}, "Downloading", "active"},
		{"a new chapter in the queue", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 39, Active: 1, New: 1, NewActive: 1}}, "1 new, queued", "new"},
		{"a back catalogue in the queue", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 37, Active: 3}}, "3 queued", "active"},
		// A chapter that came out reads differently from a back catalogue
		// that was always there, though both are "not downloaded".
		{"a new chapter is not a backlog", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 38, Waiting: 2, New: 1}}, "1 new", "new"},
		{"a backlog alone is up to date", LibraryRow{Series: following, Progress: store.SeriesProgress{Total: 40, Done: 12, Waiting: 28}}, "Up to date", "ok"},
		{"the site listed nothing", LibraryRow{Series: following}, "No chapters found", "warn"},
		{"never checked", LibraryRow{Series: store.Series{Subscribed: true}}, "Not checked yet", "muted"},
		// Not checked automatically, so "up to date" would be a claim.
		{"saved", LibraryRow{Series: store.Series{CheckedAt: now}, Progress: store.SeriesProgress{Total: 3, Done: 3}}, "Saved", "muted"},
		{"ended with everything on disk", LibraryRow{Series: ended, Progress: store.SeriesProgress{Total: 3, Done: 3}}, "Complete", "neutral"},
		{"ended with chapters left is not complete", LibraryRow{Series: ended, Progress: store.SeriesProgress{Total: 3, Done: 2, Waiting: 1}}, "Up to date", "ok"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			label, tone := c.row.Pill()
			if label != c.label || tone != c.tone {
				t.Errorf("Pill() = %q, %q; want %q, %q", label, tone, c.label, c.tone)
			}
		})
	}
}

// TestLibraryRowLines covers the meta line and the newest-chapter column.
func TestLibraryRowLines(t *testing.T) {
	hours := func(n int) sql.NullTime {
		return sql.NullTime{Time: time.Now().Add(-time.Duration(n) * time.Hour), Valid: true}
	}

	r := LibraryRow{
		Series:   store.Series{Subscribed: true, CheckedAt: hours(72), CheckFailedAt: hours(2), CheckProblem: app.ProblemMoved},
		Progress: store.SeriesProgress{Total: 20, Done: 12, Waiting: 8, New: 3, NewActive: 1, Latest: "142", LastDownload: hours(5)},
	}
	if text, tone := r.Evidence(); text != "check failed 2h ago · last worked 3d ago" || tone != "bad" {
		t.Errorf("evidence = %q %q", text, tone)
	}
	// Eight waiting, two of them new and not yet queued: six are older.
	if r.Older() != 6 {
		t.Errorf("older = %d, want 6", r.Older())
	}
	if r.Newest() != "Ch. 142" {
		t.Errorf("newest = %q", r.Newest())
	}
	if when, recent := r.Downloaded(); when != "downloaded 5h ago" || !recent {
		t.Errorf("downloaded = %q recent=%v", when, recent)
	}

	r.Series.CheckedAt = sql.NullTime{}
	if text, _ := r.Evidence(); !strings.HasSuffix(text, "never worked") {
		t.Errorf("a check that never worked: %q", text)
	}

	// Downloaded before download times were kept: say nothing rather than
	// guess, and leave the numberless chapters counted.
	old := LibraryRow{Progress: store.SeriesProgress{Total: 3, Done: 3}}
	if when, _ := old.Downloaded(); when != "" || old.Newest() != "3 chapters" {
		t.Errorf("unknown download time: %q, newest %q", when, old.Newest())
	}
	if when, _ := (LibraryRow{}).Downloaded(); when != "nothing downloaded" || (LibraryRow{}).Newest() != "—" {
		t.Errorf("nothing on disk: %q", when)
	}

	for in, want := range map[string]string{"004": "4", "0.5": "0.5", "000": "0", "2.001": "2.001", "10": "10"} {
		if got := trimNumber(in); got != want {
			t.Errorf("trimNumber(%q) = %q, want %q", in, got, want)
		}
	}

	failed := LibraryRow{Progress: store.SeriesProgress{Total: 3, Done: 1, Failed: 2, Waiting: 2}}
	if text, _ := failed.Evidence(); text != "2 chapters failed to download" {
		t.Errorf("failed downloads: %q", text)
	}
}

// TestCheckReason covers the plain words for a failing check.
func TestCheckReason(t *testing.T) {
	s := func(kind string, f store.Failure) store.Series {
		return store.Series{ModuleName: "MangaDex", CheckProblem: kind, CheckFailure: f}
	}
	cases := []struct {
		s    store.Series
		want string
	}{
		{s(app.ProblemBlocked, store.Failure{Status: 403, Challenged: true}), "MangaDex answered with an anti-bot challenge page"},
		{s(app.ProblemBlocked, store.Failure{Status: 403}), "MangaDex refused the request (HTTP 403)"},
		{s(app.ProblemMoved, store.Failure{Status: 410}), "the series' address answered 410 Not Found"},
		{s(app.ProblemDown, store.Failure{Status: 429}), "MangaDex asked atsume to slow down (HTTP 429)"},
		{s(app.ProblemDown, store.Failure{Status: 502}), "MangaDex's server returned an error (HTTP 502)"},
		{s(app.ProblemUnreachable, store.Failure{Cause: "dns"}), "MangaDex's name no longer resolves; the domain may have expired or moved"},
		{s(app.ProblemUnreachable, store.Failure{}), "MangaDex didn't answer"},
		{s(app.ProblemBroken, store.Failure{}), "atsume's reader for MangaDex hit an error"},
	}
	for _, c := range cases {
		if got := checkReason(c.s); got != c.want {
			t.Errorf("%s %+v: %q, want %q", c.s.CheckProblem, c.s.CheckFailure, got, c.want)
		}
	}
}

// TestLibraryQuery covers the tabs, filters and orders, and the addresses
// that change one of them and keep the rest.
func TestLibraryQuery(t *testing.T) {
	at := func(days int) time.Time { return time.Now().Add(-time.Duration(days) * 24 * time.Hour) }
	dl := func(days int) sql.NullTime { return sql.NullTime{Time: at(days), Valid: true} }
	row := func(title, site string, follow bool, added int, p store.SeriesProgress) LibraryRow {
		return LibraryRow{Series: store.Series{Title: title, ModuleKey: site, ModuleName: site, Subscribed: follow,
			CheckedAt: dl(0), CreatedAt: at(added)}, Progress: p}
	}
	rows := []LibraryRow{
		row("Dandadan", "MangaDex", true, 5, store.SeriesProgress{Total: 2, Done: 2, LastDownload: dl(1)}),
		row("chainsaw Man", "MangaDex", true, 1, store.SeriesProgress{Total: 2, Done: 2}),
		row("Blue Lock", "Comick", true, 9, store.SeriesProgress{Total: 2, Done: 1, Waiting: 1, New: 1, LastDownload: dl(3)}),
		row("Akira", "Comick", false, 3, store.SeriesProgress{Total: 2, Done: 2, LastDownload: dl(0)}),
	}
	titles := func(q LibraryQuery) string {
		var out []string
		for _, r := range (LibraryView{Rows: rows, Query: q}).Shown() {
			out = append(out, r.Series.Title)
		}
		return strings.Join(out, ", ")
	}

	cases := []struct {
		q    LibraryQuery
		want string
	}{
		// No download time goes last, whatever its title.
		{LibraryQuery{}, "Akira, Dandadan, Blue Lock, chainsaw Man"},
		{LibraryQuery{Sort: "title"}, "Akira, Blue Lock, chainsaw Man, Dandadan"},
		{LibraryQuery{Sort: "added"}, "chainsaw Man, Akira, Dandadan, Blue Lock"},
		{LibraryQuery{Tab: "saved"}, "Akira"},
		{LibraryQuery{Tab: "following", Site: "Comick"}, "Blue Lock"},
		{LibraryQuery{Q: "MAN"}, "chainsaw Man"},
		{LibraryQuery{Status: "new"}, "Blue Lock"},
		{LibraryQuery{Status: "uptodate", Sort: "title"}, "chainsaw Man, Dandadan"},
	}
	for _, c := range cases {
		if got := titles(c.q); got != c.want {
			t.Errorf("%+v: %s, want %s", c.q, got, c.want)
		}
	}

	q := LibraryQueryFrom(url.Values{"tab": {"saved"}, "status": {"nonsense"}, "site": {"Comick"}, "q": {" a "}, "sort": {"title"}})
	if q != (LibraryQuery{Tab: "saved", Site: "Comick", Q: "a", Sort: "title"}) {
		t.Errorf("parsed %+v; an unknown status is dropped and the search trimmed", q)
	}
	if got := q.With("site", ""); got != "/?q=a&sort=title&tab=saved" {
		t.Errorf("With = %q", got)
	}
	if got := q.Cleared(); got != "/?sort=title&tab=saved" {
		t.Errorf("Cleared = %q", got)
	}
	if got := (LibraryQuery{}).With("tab", ""); got != "/" {
		t.Errorf("an empty query is %q", got)
	}

	v := LibraryView{Rows: rows, Query: LibraryQuery{Site: "Comick", Status: "new"}}
	if chips := v.Chips(); len(chips) != 2 || chips[0].Label != "Status: New chapters" || chips[1].Remove != "/?status=new" {
		t.Errorf("chips = %+v", chips)
	}
	if items := v.SiteItems(); len(items) != 3 || items[1].Label != "Comick (2)" || !items[1].Current {
		t.Errorf("site menu = %+v", items)
	}
	if all, following, saved := v.Counts(); all != 4 || following != 3 || saved != 1 {
		t.Errorf("counts = %d %d %d", all, following, saved)
	}
}

// TestAttention covers the line above the list: what kinds of problem there
// are, and one site named when its checks are all that is wrong.
func TestAttention(t *testing.T) {
	failedAt := sql.NullTime{Time: time.Now().Add(-2 * time.Hour), Valid: true}
	check := func(site, kind string) LibraryRow {
		return LibraryRow{Series: store.Series{ModuleKey: site, ModuleName: site, Subscribed: true,
			CheckProblem: kind, CheckFailedAt: failedAt, CheckFailure: store.Failure{Status: 403, Challenged: true}}}
	}
	fine := LibraryRow{Series: store.Series{Subscribed: true, CheckedAt: failedAt}, Progress: store.SeriesProgress{Total: 1, Done: 1}}
	failed := LibraryRow{Series: store.Series{Subscribed: true}, Progress: store.SeriesProgress{Total: 2, Failed: 2, Waiting: 2}}
	empty := LibraryRow{Series: store.Series{Subscribed: true, CheckedAt: failedAt}}

	a := LibraryView{Rows: []LibraryRow{fine, check("A", app.ProblemBlocked), failed, empty}}.Attention()
	if a.Series != 3 || a.Site != "" || a.Parts() != "1 check failing · 1 with failed downloads · 1 found no chapters" {
		t.Errorf("mixed = %+v %q", a, a.Parts())
	}

	a = LibraryView{Rows: []LibraryRow{fine, check("A", app.ProblemBlocked), check("A", app.ProblemBlocked)}}.Attention()
	if a.Site != "A" || a.Headline() != "A is blocking atsume" || !a.Challenged || a.Reason != "A answered with an anti-bot challenge page" {
		t.Errorf("one site = %+v", a)
	}

	// Two sites, or two kinds, or one series alone, are not one site breaking.
	for name, rows := range map[string][]LibraryRow{
		"two sites": {check("A", app.ProblemBlocked), check("B", app.ProblemBlocked)},
		"two kinds": {check("A", app.ProblemBlocked), check("A", app.ProblemDown)},
		"alone":     {check("A", app.ProblemBlocked)},
		"and more":  {check("A", app.ProblemBlocked), check("A", app.ProblemBlocked), failed},
	} {
		if a := (LibraryView{Rows: rows}).Attention(); a.Site != "" {
			t.Errorf("%s: named site %q", name, a.Site)
		}
	}
}

// TestCheckLine covers the head's line on whether checking happens.
func TestCheckLine(t *testing.T) {
	rows := []LibraryRow{{Series: store.Series{Title: "Anything"}}}
	cases := []struct {
		name string
		v    LibraryView
		want CheckLine
	}{
		{"working", LibraryView{Rows: rows, CheckInterval: 6 * time.Hour, Swept: true,
			LastSweep: time.Now().Add(-12 * time.Minute), NextSweep: time.Now().Add(5*time.Hour + 48*time.Minute + 20*time.Second)},
			CheckLine{Dot: "ok", Text: "Checked for new chapters 12m ago · next in 5 h 48 min"}},
		{"checking", LibraryView{Rows: rows, CheckInterval: time.Hour, Checking: 3},
			CheckLine{Dot: "working", Text: "Checking for new chapters · 3 series left"}},
		{"just started", LibraryView{Rows: rows, CheckInterval: time.Hour, Uptime: time.Minute, NextSweep: time.Now().Add(2*time.Minute + 10*time.Second)},
			CheckLine{Text: "First check in 2 min", Offer: true}},
		{"stalled", LibraryView{Rows: rows, CheckInterval: 6 * time.Hour, Swept: true, LastSweep: time.Now().Add(-72 * time.Hour)},
			CheckLine{Dot: "bad", Text: "Checking has stopped — last ran 3 days ago, due every 6 hours", Offer: true}},
		{"never ran", LibraryView{Rows: rows, CheckInterval: time.Hour, Uptime: 14 * time.Hour},
			CheckLine{Dot: "bad", Text: "No check has run since atsume started 14 hours ago", Offer: true}},
		{"off", LibraryView{Rows: rows, Uptime: 14 * time.Hour},
			CheckLine{Text: "Automatic checks off", Offer: true}},
	}
	for _, c := range cases {
		if got := c.v.CheckLine(); got != c.want {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
}

// TestHomeDir covers the library folder as a reader would type it.
func TestHomeDir(t *testing.T) {
	for _, c := range []struct{ path, home, want string }{
		{"/home/a/manga/library", "/home/a", "~/manga/library"},
		{"/home/a", "/home/a/", "~"},
		{"/home/ab/manga", "/home/a", "/home/ab/manga"},
		{"/data", "", "/data"},
	} {
		if got := HomeDir(c.path, c.home); got != c.want {
			t.Errorf("HomeDir(%q, %q) = %q, want %q", c.path, c.home, got, c.want)
		}
	}
}

// TestLibraryPage renders the page with a failing site and checks what a
// reader needs from it is there.
func TestLibraryPage(t *testing.T) {
	failedAt := sql.NullTime{Time: time.Now().Add(-2 * time.Hour), Valid: true}
	check := func(id int64, title string) LibraryRow {
		return LibraryRow{Series: store.Series{ID: id, Title: title, ModuleKey: "MangaDex", ModuleName: "MangaDex", Subscribed: true,
			CheckProblem: app.ProblemBlocked, CheckFailedAt: failedAt, CheckError: "403 Forbidden",
			CheckFailure: store.Failure{Status: 403, Challenged: true}}, Progress: store.SeriesProgress{Total: 3, Done: 3}}
	}
	v := LibraryView{Rows: []LibraryRow{check(1, "Frieren"), check(2, "Dandadan")}, OthersWorking: true}
	var buf bytes.Buffer
	if err := Library(v).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	page := buf.String()
	mustContain(t, page, "MangaDex is blocking atsume", "checks failing for 2 series", "Other sites are working.",
		"Set up FlareSolverr", "Site blocked", `title="403 Forbidden"`, "Check failed 2h ago",
		"2 series · 2 following", `id="library-bulk"`, "Stop following")

	v.FlareSolverr = true
	buf.Reset()
	if err := Library(v).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	mustNotContain(t, buf.String(), "Set up FlareSolverr")

	buf.Reset()
	if err := Library(LibraryView{Sites: 658, Destination: "~/manga/library"}).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	mustContain(t, buf.String(), "Your library is empty", "one of 658 sites", "~/manga/library", "Browse sites")
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
