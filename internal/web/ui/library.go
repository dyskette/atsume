package ui

import (
	"cmp"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// LibraryRow is one series as the library lists it.
type LibraryRow struct {
	Series   store.Series
	Progress store.SeriesProgress
	// Link is the series' address on its site, with the site's address in
	// use filled in.
	Link string
}

// LibraryQuery is what the reader narrowed the library to. It lives in the
// URL, so a filtered library can be reloaded, bookmarked and linked to (the
// footer links to the Problems filter).
type LibraryQuery struct {
	// Tab is "" for every series, "following" or "saved".
	Tab string
	// Q is matched against titles.
	Q string
	// Status is one of the status filter keys, "" for any.
	Status string
	// Site is a site's key, "" for any.
	Site string
	// Sort is "" for recently downloaded, "added" or "title".
	Sort string
}

// LibraryQueryFrom reads a query from a URL's parameters, dropping values
// that name no filter so a stale link shows everything rather than nothing.
func LibraryQueryFrom(v url.Values) LibraryQuery {
	q := LibraryQuery{
		Tab:    v.Get("tab"),
		Q:      strings.TrimSpace(v.Get("q")),
		Status: v.Get("status"),
		Site:   v.Get("site"),
		Sort:   v.Get("sort"),
	}
	if q.Tab != "following" && q.Tab != "saved" {
		q.Tab = ""
	}
	if !slices.ContainsFunc(statusFilters, func(f Choice) bool { return f.Key == q.Status }) {
		q.Status = ""
	}
	if !slices.ContainsFunc(sortOrders, func(f Choice) bool { return f.Key == q.Sort }) {
		q.Sort = ""
	}
	return q
}

// With is the library's address with one parameter changed and the rest kept.
func (q LibraryQuery) With(key, value string) string {
	v := url.Values{}
	set := func(k, val string) {
		if k == key {
			val = value
		}
		if val != "" {
			v.Set(k, val)
		}
	}
	set("tab", q.Tab)
	set("q", q.Q)
	set("status", q.Status)
	set("site", q.Site)
	set("sort", q.Sort)
	if len(v) == 0 {
		return "/"
	}
	return "/?" + v.Encode()
}

// Cleared is the library's address with the filters removed, keeping the tab
// and the order.
func (q LibraryQuery) Cleared() string {
	return LibraryQuery{Tab: q.Tab, Sort: q.Sort}.With("", "")
}

// Filtered reports whether anything narrows the list beyond the tab.
func (q LibraryQuery) Filtered() bool { return q.Q != "" || q.Status != "" || q.Site != "" }

// Choice is one entry of a filter or sort menu.
type Choice struct{ Key, Label string }

var statusFilters = []Choice{
	{"", "Any"},
	{"new", "New chapters"},
	{"downloading", "Downloading"},
	{"problems", "Problems"},
	{"uptodate", "Up to date"},
	{"complete", "Complete"},
}

var sortOrders = []Choice{
	{"", "Recently downloaded"},
	{"added", "Recently added"},
	{"title", "Title A–Z"},
}

// label is a choice's label by key.
func label(choices []Choice, key string) string {
	for _, c := range choices {
		if c.Key == key {
			return c.Label
		}
	}
	return key
}

// LibraryView is the library page.
type LibraryView struct {
	// Rows is every series in the library, whatever the query shows.
	Rows  []LibraryRow
	Query LibraryQuery
	// Destination is where chapters are written, stated on the empty page
	// because it is the only thing connecting atsume to a library server.
	Destination string
	// Sites is how many sites atsume can read, for the empty page.
	Sites int

	// CheckInterval is how often a subscribed series is re-checked, and zero
	// when automatic checking is switched off.
	CheckInterval time.Duration
	// LastSweep is when the scheduler last ran, and Swept whether it has run
	// at all since the program started.
	LastSweep time.Time
	Swept     bool
	// NextSweep is when the scheduler will next run, zero when unknown.
	NextSweep time.Time
	// Checking is how many series checks are queued or running.
	Checking int
	// Uptime is how long the program has been running, which is what makes a
	// scheduler that has never run distinguishable from one that started a
	// moment ago.
	Uptime time.Duration

	// FlareSolverr reports that a solver is configured, and OthersWorking
	// that a site other than the one whose checks are failing answered
	// within the hour.
	FlareSolverr  bool
	OthersWorking bool
}

// Counts is how many series the library holds, and how they split.
func (v LibraryView) Counts() (all, following, saved int) {
	for _, r := range v.Rows {
		if r.Series.Subscribed {
			following++
		} else {
			saved++
		}
	}
	return len(v.Rows), following, saved
}

// Tabs are the library's three views of itself, with their counts.
func (v LibraryView) Tabs() []Tab {
	all, following, saved := v.Counts()
	return []Tab{
		{Label: "All", Count: all, URL: v.Query.With("tab", ""), Current: v.Query.Tab == ""},
		{Label: "Following", Count: following, URL: v.Query.With("tab", "following"), Current: v.Query.Tab == "following"},
		{Label: "Saved", Count: saved, URL: v.Query.With("tab", "saved"), Current: v.Query.Tab == "saved"},
	}
}

// Tab is one of the library's tabs.
type Tab struct {
	Label   string
	Count   int
	URL     string
	Current bool
}

// inTab is the rows the current tab holds, before any filter.
func (v LibraryView) inTab() []LibraryRow {
	var out []LibraryRow
	for _, r := range v.Rows {
		switch {
		case v.Query.Tab == "following" && !r.Series.Subscribed,
			v.Query.Tab == "saved" && r.Series.Subscribed:
			continue
		}
		out = append(out, r)
	}
	return out
}

// TabTotal is how many series the current tab holds.
func (v LibraryView) TabTotal() int { return len(v.inTab()) }

// Shown is the rows the tab and filters leave, in the chosen order.
func (v LibraryView) Shown() []LibraryRow {
	q := v.Query
	needle := strings.ToLower(q.Q)
	var out []LibraryRow
	for _, r := range v.inTab() {
		if needle != "" && !strings.Contains(strings.ToLower(r.Series.Title), needle) {
			continue
		}
		if q.Site != "" && r.Site() != q.Site {
			continue
		}
		if q.Status != "" && !r.Matches(q.Status) {
			continue
		}
		out = append(out, r)
	}
	title := func(a, b LibraryRow) int {
		return cmp.Compare(strings.ToLower(a.Series.Title), strings.ToLower(b.Series.Title))
	}
	switch q.Sort {
	case "title":
		slices.SortStableFunc(out, title)
	case "added":
		slices.SortStableFunc(out, func(a, b LibraryRow) int {
			return cmp.Or(b.Series.CreatedAt.Compare(a.Series.CreatedAt), title(a, b))
		})
	default:
		// A series with no recorded download goes last: either nothing is on
		// disk, or it all came in before download times were kept, and
		// guessing would sort it as if the time were known.
		slices.SortStableFunc(out, func(a, b LibraryRow) int {
			at, bt := a.Progress.LastDownload, b.Progress.LastDownload
			switch {
			case at.Valid && bt.Valid:
				return cmp.Or(bt.Time.Compare(at.Time), title(a, b))
			case at.Valid:
				return -1
			case bt.Valid:
				return 1
			}
			return title(a, b)
		})
	}
	return out
}

// StatusLabel is the status filter's current label.
func (v LibraryView) StatusLabel() string { return label(statusFilters, v.Query.Status) }

// SortLabel is the order's current label.
func (v LibraryView) SortLabel() string { return label(sortOrders, v.Query.Sort) }

// SiteLabel is the site filter's current label.
func (v LibraryView) SiteLabel() string {
	if v.Query.Site == "" {
		return "Any"
	}
	return v.Query.Site
}

// MenuItem is one entry of a filter or sort menu.
type MenuItem struct {
	Label   string
	URL     string
	Current bool
}

// StatusItems is the status filter's menu.
func (v LibraryView) StatusItems() []MenuItem {
	return v.items(statusFilters, "status", v.Query.Status)
}

// SortItems is the order's menu.
func (v LibraryView) SortItems() []MenuItem { return v.items(sortOrders, "sort", v.Query.Sort) }

// SiteItems is the site filter's menu: every site the library uses, with how
// many series come from it.
func (v LibraryView) SiteItems() []MenuItem {
	counts := map[string]int{}
	for _, r := range v.Rows {
		counts[r.Site()]++
	}
	sites := make([]string, 0, len(counts))
	for s := range counts {
		sites = append(sites, s)
	}
	slices.SortFunc(sites, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	choices := []Choice{{"", "Any"}}
	for _, s := range sites {
		choices = append(choices, Choice{s, fmt.Sprintf("%s (%d)", s, counts[s])})
	}
	return v.items(choices, "site", v.Query.Site)
}

func (v LibraryView) items(choices []Choice, key, current string) []MenuItem {
	out := make([]MenuItem, 0, len(choices))
	for _, c := range choices {
		out = append(out, MenuItem{Label: c.Label, URL: v.Query.With(key, c.Key), Current: c.Key == current})
	}
	return out
}

// FilterChip is one active filter, with the address that removes it.
type FilterChip struct {
	Label  string
	Remove string
}

// Chips are the active filters.
func (v LibraryView) Chips() []FilterChip {
	q := v.Query
	var out []FilterChip
	if q.Q != "" {
		out = append(out, FilterChip{"Title: " + q.Q, q.With("q", "")})
	}
	if q.Status != "" {
		out = append(out, FilterChip{"Status: " + v.StatusLabel(), q.With("status", "")})
	}
	if q.Site != "" {
		out = append(out, FilterChip{"Site: " + q.Site, q.With("site", "")})
	}
	return out
}

// Site is the key of the site the series came from.
func (r LibraryRow) Site() string { return r.Series.Key() }

// CheckFailing reports whether the latest check of the series failed.
func (r LibraryRow) CheckFailing() bool { return r.Series.CheckFailedAt.Valid }

// FoundNothing is a check that worked and found no chapters, which usually
// means a login the site wants or an address that moved.
func (r LibraryRow) FoundNothing() bool {
	return !r.CheckFailing() && r.Progress.Total == 0 && r.Series.CheckedAt.Valid
}

// Problem reports whether the series needs the reader: its check fails, a
// download failed, or the site listed nothing.
func (r LibraryRow) Problem() bool {
	return r.CheckFailing() || r.Progress.Failed > 0 || r.FoundNothing()
}

// Complete is a series its site says has ended, with nothing left to fetch.
func (r LibraryRow) Complete() bool {
	s := strings.ToLower(r.Series.Status)
	ended := strings.Contains(s, "complet") || strings.Contains(s, "finished") || strings.Contains(s, "ended")
	return ended && r.Progress.Waiting == 0 && r.Progress.Active == 0
}

// Matches reports whether the row passes a status filter.
func (r LibraryRow) Matches(status string) bool {
	switch status {
	case "new":
		return r.Progress.New > 0
	case "downloading":
		return r.Progress.Active > 0
	case "problems":
		return r.Problem()
	case "uptodate":
		_, tone := r.Pill()
		return tone == "ok"
	case "complete":
		label, _ := r.Pill()
		return label == "Complete"
	}
	return true
}

// Pill is the short answer to "is this one up to date", which is the question
// the library exists to answer, and its tone. What needs the reader comes
// first: a failure waits for them, a download finishes on its own.
func (r LibraryRow) Pill() (label, tone string) {
	p := r.Progress
	switch {
	case r.CheckFailing():
		return problemLabel(r.Series.CheckProblem), "bad"
	case p.Failed > 0:
		return fmt.Sprintf("%d failed", p.Failed), "bad"
	case p.Downloading > 0:
		return "Downloading", "active"
	case p.Active > 0 && p.NewActive > 0:
		return fmt.Sprintf("%d new, queued", p.NewActive), "new"
	case p.Active > 0:
		return fmt.Sprintf("%d queued", p.Active), "active"
	// A chapter that came out is not the same event as a back catalogue that
	// was always there, and saying "24 not downloaded" for both is what made
	// the page unable to answer the only question it is opened for.
	case p.New > 0:
		return fmt.Sprintf("%d new", p.New), "new"
	case r.FoundNothing():
		return "No chapters found", "warn"
	case p.Total == 0:
		return "Not checked yet", "muted"
	// A saved series is not checked, so calling it up to date would be a
	// claim nothing backs.
	case !r.Series.Subscribed:
		return "Saved", "muted"
	case r.Complete():
		return "Complete", "neutral"
	}
	return "Up to date", "ok"
}

// problemLabel names a failing check by what went wrong.
func problemLabel(kind string) string {
	switch kind {
	case app.ProblemBlocked:
		return "Site blocked"
	case app.ProblemMoved:
		return "Site moved"
	case app.ProblemDown:
		return "Site down"
	case app.ProblemUnreachable:
		return "Unreachable"
	case app.ProblemBroken:
		return "Reader broken"
	}
	return "Check failing"
}

// Older is how many chapters published before the series was followed are
// not on disk.
func (r LibraryRow) Older() int {
	p := r.Progress
	return max(0, p.Waiting-(p.New-p.NewActive))
}

// Status is the series' publication status, capitalised.
func (r LibraryRow) Status() string { return upper(r.Series.Status) }

// Evidence is what backs a problem pill on the meta line, with its tone, ""
// when there is no problem.
func (r LibraryRow) Evidence() (text, tone string) {
	s := r.Series
	switch {
	case r.CheckFailing():
		out := "check failed " + ago(s.CheckFailedAt.Time)
		if s.CheckedAt.Valid {
			out += " · last worked " + ago(s.CheckedAt.Time)
		} else {
			out += " · never worked"
		}
		return out, "bad"
	case r.Progress.Failed > 0:
		return Count(r.Progress.Failed, "chapter", "chapters") + " failed to download", "bad"
	case r.FoundNothing():
		return "the site listed no chapters " + ago(s.CheckedAt.Time), "warn"
	}
	return "", ""
}

// Newest is the newest chapter on disk, "—" when nothing is.
func (r LibraryRow) Newest() string {
	switch {
	case r.Progress.Latest != "":
		return "Ch. " + trimNumber(r.Progress.Latest)
	case r.Progress.Done > 0:
		return Count(r.Progress.Done, "chapter", "chapters")
	}
	return "—"
}

// trimNumber drops the leading zeros a site pads chapter numbers with, so
// "004" reads 4 and "0.5" stays 0.5.
func trimNumber(n string) string {
	t := strings.TrimLeft(n, "0")
	if t == "" || t[0] < '0' || t[0] > '9' {
		return "0" + t
	}
	return t
}

// Downloaded says when a chapter last arrived on disk, "" when that is not
// known, and whether it was within the day.
func (r LibraryRow) Downloaded() (text string, recent bool) {
	switch {
	case r.Progress.LastDownload.Valid:
		t := r.Progress.LastDownload.Time
		return "downloaded " + ago(t), time.Since(t) < 24*time.Hour
	case r.Progress.Done == 0:
		return "nothing downloaded", false
	}
	return "", false
}

// URL is the series page.
func (r LibraryRow) URL() string { return fmt.Sprintf("/series/%d", r.Series.ID) }

// Reason says in plain words why the latest check failed.
func (r LibraryRow) Reason() string { return checkReason(r.Series) }

// checkReason says in plain words why a series' latest check failed.
func checkReason(s store.Series) string {
	site, f := s.ModuleName, s.CheckFailure
	switch s.CheckProblem {
	case app.ProblemBlocked:
		if f.Challenged {
			return site + " answered with an anti-bot challenge page"
		}
		if f.Status > 0 {
			return fmt.Sprintf("%s refused the request (HTTP %d)", site, f.Status)
		}
		return site + " refused the request"
	case app.ProblemMoved:
		return fmt.Sprintf("the series' address answered %d Not Found", cmp.Or(f.Status, 404))
	case app.ProblemDown:
		if f.Status == 429 {
			return site + " asked atsume to slow down (HTTP 429)"
		}
		if f.Status > 0 {
			return fmt.Sprintf("%s's server returned an error (HTTP %d)", site, f.Status)
		}
		return site + "'s server returned an error"
	case app.ProblemUnreachable:
		switch f.Cause {
		case "dns":
			return site + "'s name no longer resolves; the domain may have expired or moved"
		case "refused":
			return site + "'s server refused the connection"
		case "timeout":
			return site + " didn't answer in time"
		}
		return site + " didn't answer"
	case app.ProblemBroken:
		return "atsume's reader for " + site + " hit an error"
	}
	return "the check didn't finish"
}

// Attention is what the library needs the reader for, counted over the whole
// library whatever the filters show, so a filter cannot hide a broken series.
type Attention struct {
	Series    int
	Checks    int
	Downloads int
	Empty     int

	// Site is set when every problem is a failing check of one site, of one
	// kind, and there is more than one: the common case of a site breaking,
	// whose fix is on that site's page rather than on each series.
	Site       string
	SiteName   string
	Kind       string
	Since      time.Time
	Challenged bool
	// Reason is the site's failure in plain words.
	Reason string
}

// Attention counts the series that need the reader.
func (v LibraryView) Attention() Attention {
	var a Attention
	sites, kinds := map[string]bool{}, map[string]bool{}
	var first LibraryRow
	for _, r := range v.Rows {
		if !r.Problem() {
			continue
		}
		a.Series++
		switch {
		case r.CheckFailing():
			a.Checks++
			if a.Checks == 1 {
				first = r
			}
			sites[r.Site()], kinds[r.Series.CheckProblem] = true, true
			if t := r.Series.CheckFailedAt.Time; a.Since.IsZero() || t.Before(a.Since) {
				a.Since = t
			}
			a.Challenged = a.Challenged || r.Series.CheckFailure.Challenged
		case r.Progress.Failed > 0:
			a.Downloads++
		default:
			a.Empty++
		}
	}
	if a.Checks > 1 && a.Checks == a.Series && len(sites) == 1 && len(kinds) == 1 {
		a.Site, a.SiteName, a.Kind = first.Site(), first.Series.ModuleName, first.Series.CheckProblem
		a.Reason = first.Reason()
	}
	return a
}

// Parts lists what kinds of problem there are, for the attention line.
func (a Attention) Parts() string {
	var parts []string
	if a.Checks > 0 {
		parts = append(parts, Count(a.Checks, "check failing", "checks failing"))
	}
	if a.Downloads > 0 {
		parts = append(parts, fmt.Sprintf("%d with failed downloads", a.Downloads))
	}
	if a.Empty > 0 {
		parts = append(parts, fmt.Sprintf("%d found no chapters", a.Empty))
	}
	return strings.Join(parts, " · ")
}

// Headline names a site whose checks all fail by what is wrong with it.
func (a Attention) Headline() string {
	site := a.SiteName
	switch a.Kind {
	case app.ProblemBlocked:
		return site + " is blocking atsume"
	case app.ProblemMoved:
		return site + " has moved"
	case app.ProblemDown:
		return site + " is down"
	case app.ProblemUnreachable:
		return site + " can't be reached"
	case app.ProblemBroken:
		return "atsume's reader for " + site + " is broken"
	}
	return "Checks of " + site + " are failing"
}

// problemsURL is the library narrowed to what needs the reader.
const problemsURL = "/?status=problems"

// ProblemsURL is the library narrowed to what needs the reader.
func (v LibraryView) ProblemsURL() string { return problemsURL }

// CheckLine is the head's one line on whether checking is happening.
type CheckLine struct {
	// Dot is the status dot's class: "ok", "working", "bad" or "".
	Dot  string
	Text string
	// Offer is whether Check now belongs on the line.
	Offer bool
}

// CheckLine says whether automatic checking is happening, in the terms the
// reader set it in.
//
// A scheduler that has died looks exactly like one with nothing due: every
// row keeps its last known state and the page goes on implying it is current.
func (v LibraryView) CheckLine() CheckLine {
	switch {
	case v.Checking > 0:
		return CheckLine{Dot: "working", Text: "Checking for new chapters · " + Count(v.Checking, "series", "series") + " left"}
	case v.CheckInterval <= 0:
		return CheckLine{Text: "Automatic checks off", Offer: true}
	}
	if stalled, why := v.Stalled(); stalled {
		return CheckLine{Dot: "bad", Text: why, Offer: true}
	}
	if !v.Swept {
		text := "First check soon"
		if d := time.Until(v.NextSweep); !v.NextSweep.IsZero() && d > 0 {
			text = "First check in " + within(d)
		}
		return CheckLine{Text: text, Offer: true}
	}
	text := "Checked for new chapters " + ago(v.LastSweep)
	if d := time.Until(v.NextSweep); !v.NextSweep.IsZero() && d > 0 {
		text += " · next in " + within(d)
	}
	return CheckLine{Dot: "ok", Text: text}
}

// Stalled reports whether automatic checking has stopped happening, and says
// so. The grace is two intervals, so a sweep running late is not an alarm.
func (v LibraryView) Stalled() (bool, string) {
	if v.CheckInterval <= 0 || len(v.Rows) == 0 {
		return false, ""
	}
	grace := 2 * v.CheckInterval
	if !v.Swept {
		// Nothing has swept yet. That is normal for the first minute after a
		// restart and not normal an interval later.
		if v.Uptime < grace {
			return false, ""
		}
		return true, fmt.Sprintf("No check has run since atsume started %s ago",
			humanDuration(v.Uptime.Truncate(time.Minute)))
	}
	since := time.Since(v.LastSweep)
	if since < grace {
		return false, ""
	}
	return true, fmt.Sprintf("Checking has stopped — last ran %s ago, due every %s",
		humanDuration(since.Truncate(time.Minute)), humanDuration(v.CheckInterval))
}

// within renders a wait as hours and minutes, "5 h 48 min".
func within(d time.Duration) string {
	d = d.Round(time.Minute)
	h, m := int(d/time.Hour), int(d%time.Hour/time.Minute)
	switch {
	case d < time.Minute:
		return "under a minute"
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		return fmt.Sprintf("%d h", h)
	}
	return fmt.Sprintf("%d h %d min", h, m)
}

// HomeDir shortens the library folder to start with ~ when it is under the
// home directory, as a reader would type it.
func HomeDir(path, home string) string {
	home = strings.TrimSuffix(home, "/")
	if home == "" {
		return path
	}
	if path == home {
		return "~"
	}
	if rest, ok := strings.CutPrefix(path, home+"/"); ok {
		return "~/" + rest
	}
	return path
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
