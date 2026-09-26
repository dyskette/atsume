package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// BrowseView is a site's page: its catalogue as stored, how the latest read
// of it went, and the titles the current search matches.
//
// The list is read once and kept. It used to be fetched on every visit and
// thrown away: MangaToon's module pages the site internally and takes three
// minutes to hand back 2,677 titles, so looking twice cost six minutes of
// someone else's bandwidth and a reader who could not leave the page.
type BrowseView struct {
	Module   string
	Category string
	// SiteURL is the address atsume reads the site at, for Open site.
	SiteURL string
	// Catalogue is what is stored, including when and how it was read.
	Catalogue store.SiteCatalogue
	// Indexing is a read running or about to, and Progress how far along
	// it is once it runs.
	Indexing bool
	Progress app.ReadProgress

	Titles []store.SiteTitle
	// Found is how many match the query, which is not how many are shown.
	Found       int
	Query       string
	HideLibrary bool
	// HiddenMatches is how many titles the library filter hides from a
	// search that shows none, so the page can say so rather than suggest the
	// site lacks them.
	HiddenMatches int
	Offset        int
	Limit         int

	// Tracked maps a title's URL to the series in the library for it.
	Tracked map[string]int64

	// FlareSolverr reports that a solver is configured, OthersWorking that
	// another site answered within the hour, and Modules the modules'
	// commit date and the file this site's module is in.
	FlareSolverr  bool
	OthersWorking bool
	ModulesDate   time.Time
	ModuleFile    string
}

// staleAfter is when a downloaded catalogue is old enough to say so plainly
// rather than leave the reader to work it out from a month and a year.
const staleAfter = 180 * 24 * time.Hour

// PageURL is the site's page.
func (v BrowseView) PageURL() string { return SiteURL(v.Module, "") }

// ListURL is where the rows for the current search are fetched from.
func (v BrowseView) ListURL() string {
	return SiteURL(v.Module, "/list?"+v.listQuery(v.Offset).Encode())
}

// MoreURL fetches the next run of rows to append to what is on screen.
func (v BrowseView) MoreURL() string {
	q := v.listQuery(v.Offset + v.Limit)
	q.Set("rows", "1")
	return SiteURL(v.Module, "/list?"+q.Encode())
}

// ShowLibraryURL is the current search with the library filter off.
func (v BrowseView) ShowLibraryURL() string {
	q := url.Values{}
	if v.Query != "" {
		q.Set("q", v.Query)
	}
	if len(q) == 0 {
		return v.PageURL()
	}
	return v.PageURL() + "?" + q.Encode()
}

func (v BrowseView) listQuery(offset int) url.Values {
	q := url.Values{}
	q.Set("q", v.Query)
	q.Set("offset", fmt.Sprint(offset))
	if v.HideLibrary {
		q.Set("hide", "1")
	}
	return q
}

// More reports whether anything is left beyond the rows on screen.
func (v BrowseView) More() bool { return v.Offset+len(v.Titles) < v.Found }

// Empty reports that there is no catalogue at all yet, which is a different
// thing from a catalogue with nothing in it.
func (v BrowseView) Empty() bool { return !v.Catalogue.Exists && !v.Indexing }

// Shared reports a list downloaded from a published snapshot rather than
// read from the site.
func (v BrowseView) Shared() bool { return v.Catalogue.Exists && !v.Catalogue.FromSite() }

// StaleSnapshot reports whether a downloaded catalogue is old enough that a
// reader should be told outright.
func (v BrowseView) StaleSnapshot() bool {
	if !v.Shared() || v.Catalogue.Age().IsZero() {
		return false
	}
	return time.Since(v.Catalogue.Age()) > staleAfter
}

// Refreshing is a read running over a list already here.
func (v BrowseView) Refreshing() bool { return v.Indexing && v.Catalogue.OverList }

// Failed is a latest read that ran into something, as opposed to one that
// finished or was stopped.
func (v BrowseView) Failed() bool {
	p := v.Catalogue.Problem
	return !v.Indexing && p != "" && p != app.ProblemStopped
}

// Incomplete is a latest read that did not get to the end and left titles
// to browse, which is when the page explains what is missing.
func (v BrowseView) Incomplete() bool {
	return !v.Indexing && v.Catalogue.Exists && !v.Catalogue.Complete && v.Catalogue.Titles > 0
}

// Resumable is an unfinished read that can carry on from where it stopped.
func (v BrowseView) Resumable() bool { return v.Catalogue.Resume.Dir >= 0 }

// Dot is the status line's dot: working, ok, warn or bad.
func (v BrowseView) Dot() string {
	switch {
	case v.Indexing:
		return "working"
	case !v.Catalogue.Exists:
		return ""
	case v.Failed() && v.Catalogue.Titles == 0:
		return "bad"
	case !v.Catalogue.Complete || v.Catalogue.Titles == 0 || v.StaleSnapshot():
		return "warn"
	}
	return "ok"
}

// Status is the line under the site's name.
func (v BrowseView) Status() string {
	c := v.Catalogue
	switch {
	case v.Indexing:
		head := "Reading the site"
		if v.Refreshing() {
			head = "Refreshing"
		}
		out := head + " · page " + fmt.Sprint(max(v.Progress.Page, 1))
		if v.Progress.Estimate > 0 {
			out += fmt.Sprintf(" of about %d", v.Progress.Estimate)
		}
		if !v.Refreshing() {
			out += " · " + Count(v.Progress.Titles, "title", "titles") + " so far"
		}
		return out
	case !c.Exists:
		return "Not read yet"
	case v.Failed() && c.Titles == 0:
		return "Last read failed · " + ago(c.BuiltAt)
	}
	out := Count(c.Titles, "title", "titles")
	switch {
	case v.Shared():
		if !c.Age().IsZero() {
			out += " · updated " + c.Age().Format("January 2006")
		}
	case !c.Complete:
		out += " · read incomplete · " + ago(c.BuiltAt)
	default:
		out += " · read " + ago(c.BuiltAt)
	}
	return out
}

// host is the host part of an address, or the address itself.
func host(addr string) string {
	if u, err := url.Parse(addr); err == nil && u.Host != "" {
		return u.Host
	}
	return addr
}

// Reason says in plain words what ended the latest read.
func (v BrowseView) Reason() string {
	c, site := v.Catalogue, v.Module
	switch c.Problem {
	case app.ProblemStopped:
		return "you stopped the read"
	case app.ProblemBlocked:
		if c.Challenged {
			return site + " served an anti-bot challenge page"
		}
		return fmt.Sprintf("%s blocked the request (HTTP %d)", site, c.Status)
	case app.ProblemMoved:
		return fmt.Sprintf("the address atsume uses answered %d Not Found", c.Status)
	case app.ProblemDown:
		if c.Status == 429 {
			return site + " asked atsume to slow down (HTTP 429)"
		}
		return fmt.Sprintf("the site's server returned an error (HTTP %d)", c.Status)
	case app.ProblemUnreachable:
		switch c.Cause {
		case "dns":
			return "the name " + host(v.SiteURL) + " no longer resolves; the domain may have expired or moved"
		case "refused":
			return "the server refused the connection"
		case "timeout":
			return "the site didn't answer in time"
		}
		return "the site didn't answer"
	case app.ProblemBroken:
		return "the module that reads it hit an error"
	}
	return "the read didn't finish"
}

// StoppedAt says where an unfinished read stopped.
func (v BrowseView) StoppedAt() string {
	out := fmt.Sprintf("page %d", v.Catalogue.Steps+1)
	if v.Catalogue.Pages > 0 {
		out += fmt.Sprintf(" of about %d", v.Catalogue.Pages)
	}
	return out
}

// Retry says what atsume will do by itself about a failed read, "" when it
// will do nothing and never tried.
func (v BrowseView) Retry() string {
	c := v.Catalogue
	switch {
	case !c.RetryAt.IsZero() && time.Until(c.RetryAt) > 0:
		return "atsume will try again automatically in " + roughly(time.Until(c.RetryAt)) + "."
	case c.Retries > 0 && (c.Problem == app.ProblemDown || c.Problem == app.ProblemUnreachable):
		return fmt.Sprintf("atsume tried %s and stopped; try again when the site is back.",
			Count(c.Retries+1, "time", "times"))
	}
	return ""
}

// roughly renders a wait the way someone would say it.
func roughly(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return fmt.Sprintf("%d min", int(d.Round(time.Minute)/time.Minute))
	default:
		return plural(int(d.Round(time.Hour)/time.Hour), "hour")
	}
}

// KeptTitles is an incomplete read that found no titles and kept the ones
// already here, as opposed to one that stopped partway.
func (v BrowseView) KeptTitles() bool { return v.Incomplete() && !v.Resumable() }

// NoMatch is a search that shows nothing on a list that has titles.
func (v BrowseView) NoMatch() bool {
	return v.Found == 0 && v.Catalogue.Titles > 0 && (v.Query != "" || v.HideLibrary)
}

// OfferRefresh reports whether suggesting a refresh could help a search that
// found nothing: for a shared list, or one read a day or more ago.
func (v BrowseView) OfferRefresh() bool {
	if v.Indexing {
		return false
	}
	return v.Shared() || time.Since(v.Catalogue.BuiltAt) >= 24*time.Hour
}

// SearchPlaceholder counts what the search runs over.
func (v BrowseView) SearchPlaceholder() string {
	if v.Catalogue.Titles == 0 {
		return "Search titles on " + v.Module
	}
	return fmt.Sprintf("Search %s on %s", Count(v.Catalogue.Titles, "title", "titles"), v.Module)
}

// ListedWhen says when the list being searched was made, for a search with
// no match.
func (v BrowseView) ListedWhen() string {
	c := v.Catalogue
	if v.Shared() && !c.Age().IsZero() {
		return "listed in " + c.Age().Format("January 2006")
	}
	return "read " + ago(c.BuiltAt)
}

// ModulesLine names the module file and how current the modules are.
func (v BrowseView) ModulesLine() string {
	var parts []string
	if v.ModuleFile != "" {
		parts = append(parts, "Module "+v.ModuleFile+".lua")
	}
	if !v.ModulesDate.IsZero() {
		parts = append(parts, "modules updated "+v.ModulesDate.Format("2 Jan 2006"))
	}
	return strings.Join(parts, " · ")
}

// flareSolverrDocs is where setting up FlareSolverr is explained.
const flareSolverrDocs = "https://github.com/dyskette/atsume#anti-bot-challenges"
