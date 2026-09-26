package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// SitesView is the Sites page: the sites the library uses, then every site
// in a table with how its last read went.
type SitesView struct {
	Ref   string
	Total int
	// Yours is the sites the library uses, as cards.
	Yours []SiteCard

	Query        string
	Category     string
	HideProblems bool
	ShowAll      bool
	// Chips are the largest categories, with the rest under More.
	Chips, MoreChips []CategoryChip
	// Rows are the sites the search matches, as many as are shown, and
	// Found how many it matches.
	Rows  []SiteRow
	Found int

	// ModulesDate is when the modules' commit was made, and Updated a page
	// shown right after updating them.
	ModulesDate time.Time
	Updated     bool
}

// SiteCard is a site the library uses.
type SiteCard struct {
	Site      string
	Following int
	Titles    int
	// Line is the one thing worth knowing about it now, and Tone its
	// colour: ok, warn or bad.
	Line, Tone string
}

// CategoryChip is a category and how many sites it holds.
type CategoryChip struct {
	Name string
	N    int
}

// SiteRow is one site in the table.
type SiteRow struct {
	Entry     app.ModuleEntry
	Catalogue store.SiteCatalogue
	// Status is what its last read says about it, and Tone the dot's colour:
	// ok, warn, bad or "" for grey.
	Status, Tone string
}

// uncategorised is where modules that declare nothing go. Named rather than
// hidden: a reader looking for a site they know would otherwise think it
// missing.
const uncategorised = "Uncategorised"

// siteRows is how many sites the table shows before "Show all".
const siteRows = 100

// chipCount is how many categories get a chip of their own.
const chipCount = 6

// SitesInput is what BuildSites needs.
type SitesInput struct {
	Catalogue  app.Catalogue
	Catalogues map[string]store.SiteCatalogue
	Usage      map[string]store.SiteUse

	Query, Category string
	HideProblems    bool
	ShowAll         bool
	ModulesDate     time.Time
	Updated         bool
}

// BuildSites filters the catalogue into the page.
func BuildSites(in SitesInput) SitesView {
	v := SitesView{
		Ref: in.Catalogue.Ref, Total: len(in.Catalogue.Entries),
		Query: strings.TrimSpace(in.Query), Category: in.Category,
		HideProblems: in.HideProblems, ShowAll: in.ShowAll, ModulesDate: in.ModulesDate, Updated: in.Updated,
	}

	counts := map[string]int{}
	q := strings.ToLower(v.Query)
	for _, e := range in.Catalogue.Entries {
		cat := categoryOf(e)
		counts[cat]++
		row := siteRow(e, in.Catalogues[e.Site])
		if u, ok := in.Usage[e.Site]; ok {
			v.Yours = append(v.Yours, siteCard(e, u, row))
		}
		switch {
		case q != "" && !strings.Contains(strings.ToLower(e.Site), q):
			continue
		case v.Category != "" && cat != v.Category:
			continue
		case v.HideProblems && (row.Tone == "bad" || row.Tone == "warn"):
			continue
		}
		v.Found++
		if v.ShowAll || len(v.Rows) < siteRows {
			v.Rows = append(v.Rows, row)
		}
	}

	names := make([]string, 0, len(counts))
	for name := range counts {
		names = append(names, name)
	}
	// Largest first: the categories most people want are the big ones, and
	// alphabetical order would bury them. Whatever has no category goes last.
	sort.Slice(names, func(i, j int) bool {
		a, b := names[i], names[j]
		if (a == uncategorised) != (b == uncategorised) {
			return b == uncategorised
		}
		if counts[a] != counts[b] {
			return counts[a] > counts[b]
		}
		return a < b
	})
	for i, name := range names {
		chip := CategoryChip{Name: name, N: counts[name]}
		if i < chipCount {
			v.Chips = append(v.Chips, chip)
		} else {
			v.MoreChips = append(v.MoreChips, chip)
		}
	}
	return v
}

func categoryOf(e app.ModuleEntry) string {
	if e.Category == "" {
		return uncategorised
	}
	return e.Category
}

// siteRow says what a site's last read tells about it.
func siteRow(e app.ModuleEntry, c store.SiteCatalogue) SiteRow {
	row := SiteRow{Entry: e, Catalogue: c}
	switch {
	case !e.Available():
		row.Status, row.Tone = "Reader broken", "bad"
	case !c.Exists:
		row.Status = "Not read yet"
	case c.Problem == app.ProblemBlocked:
		row.Status, row.Tone = "Blocked", "bad"
	case c.Problem == app.ProblemMoved:
		row.Status, row.Tone = fmt.Sprintf("Moved? (%d)", c.Status), "warn"
	case c.Problem == app.ProblemDown:
		row.Status, row.Tone = "Site down (5xx)", "warn"
		if c.Status == 429 {
			row.Status = "Slowing atsume down (429)"
		}
	case c.Problem == app.ProblemUnreachable:
		row.Status, row.Tone = "Unreachable", "bad"
	case c.Problem == app.ProblemBroken:
		row.Status, row.Tone = "Reader broken", "bad"
	case !c.Complete:
		row.Status, row.Tone = "Incomplete read", "warn"
	case c.Titles == 0:
		row.Status = "Listed no titles"
	case !c.FromSite():
		row.Status = "Shared list"
	default:
		row.Status, row.Tone = "Working", "ok"
	}
	return row
}

// siteCard says the one thing worth knowing about a site the library uses:
// a check failing, chapters that arrived today, a read that did not finish,
// or when it was last read.
func siteCard(e app.ModuleEntry, u store.SiteUse, row SiteRow) SiteCard {
	card := SiteCard{Site: e.Site, Following: u.Following, Titles: row.Catalogue.Titles, Tone: "ok"}
	switch {
	case u.CheckError != "":
		card.Line, card.Tone = "Chapter checks failing", "bad"
	case !e.Available():
		card.Line, card.Tone = "Reader broken", "bad"
	case u.NewToday > 0:
		card.Line = Count(u.NewToday, "new chapter", "new chapters") + " today"
	case row.Catalogue.Exists && !row.Catalogue.Complete:
		card.Line, card.Tone = "Last read incomplete", "warn"
	case row.Catalogue.Exists:
		card.Line = "Read " + ago(row.Catalogue.BuiltAt)
	case !u.CheckedAt.IsZero():
		card.Line = "Checked " + ago(u.CheckedAt)
	}
	return card
}

// LastRead is when a site's list was last read, or how old a shared one is.
func (r SiteRow) LastRead() string {
	c := r.Catalogue
	switch {
	case !c.Exists:
		return "—"
	case !c.FromSite() && !c.Age().IsZero():
		return c.Age().Format("Jan 2006")
	}
	return ago(c.BuiltAt)
}

// TitlesText is a site's title count, or a dash when none was read.
func (r SiteRow) TitlesText() string {
	if !r.Catalogue.Exists || (r.Catalogue.Titles == 0 && r.Catalogue.Problem != "") {
		return "—"
	}
	return fmt.Sprint(r.Catalogue.Titles)
}

// URL is this page with a different filter.
func (v SitesView) URL(category string, hideProblems, all bool) string {
	q := url.Values{}
	if v.Query != "" {
		q.Set("q", v.Query)
	}
	if category != "" {
		q.Set("cat", category)
	}
	if hideProblems {
		q.Set("problems", "hide")
	}
	if all {
		q.Set("all", "1")
	}
	if len(q) == 0 {
		return "/modules"
	}
	return "/modules?" + q.Encode()
}

// Counts is the line beside the heading.
func (v SitesView) Counts() string {
	return fmt.Sprintf("%d available · %d you use", v.Total, len(v.Yours))
}
