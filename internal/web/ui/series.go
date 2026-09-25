package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/store"
)

// ChapterCounts is the state of a series at a glance.
//
// "How many do I have, is anything happening, did anything break" is what the
// page exists to answer, and none of it is derivable from a list of rows by
// looking.
type ChapterCounts struct {
	Total       int
	Done        int
	Pending     int
	Queued      int
	Downloading int
	Failed      int
}

// CountChapters summarises a chapter list.
func CountChapters(chapters []store.Chapter) ChapterCounts {
	var c ChapterCounts
	c.Total = len(chapters)
	for _, ch := range chapters {
		switch ch.State {
		case store.ChapterDone:
			c.Done++
		case store.ChapterQueued:
			c.Queued++
		case store.ChapterDownloading:
			c.Downloading++
		case store.ChapterFailed:
			c.Failed++
		default:
			c.Pending++
		}
	}
	return c
}

// Waiting is everything not yet on disk.
func (c ChapterCounts) Waiting() int { return c.Pending + c.Failed }

// SeriesView is everything the series page renders.
//
// One page serves a series in every state. Series.ID is 0 for one not in the
// library, which is shown from what its site returned just now: its chapters
// are in Listed rather than Chapters, and Module and SeriesURL are what
// following or downloading it needs.
type SeriesView struct {
	Series   store.Series
	Chapters []store.Chapter
	Counts   ChapterCounts
	// Module is the site's module key and SeriesURL the series' address as
	// the module gives it.
	Module    string
	SeriesURL string
	// Listed is the chapter list of a series not in the library.
	Listed []ListedChapter
	// Destination is the directory chapters are written to.
	Destination string
	// SiteURL is the series' address on the site it came from. The stored
	// link has its host stripped, so rendering that directly pointed "open
	// on site" back at atsume.
	SiteURL string
	// CheckInterval is how often a followed series is re-checked. Zero means
	// automatic checking is switched off entirely.
	CheckInterval time.Duration

	// SiteNeedsLogin and SiteHasCredentials decide whether an empty chapter
	// list is worth blaming on a missing account.
	SiteNeedsLogin     bool
	SiteHasCredentials bool

	// Missing holds the chapters atsume believes it wrote whose file is no
	// longer there. The library directory belongs to whatever reads it, and
	// a file can leave without atsume being told.
	Missing map[int64]bool

	// Rows holds each chapter's place in line or download progress.
	Rows map[int64]RowState
	// Filter, Oldest and ShowAll are how the chapter list is shown: which
	// chapters, oldest or newest first, and all of them or the first few.
	Filter  string
	Oldest  bool
	ShowAll bool
}

// ListedChapter is a chapter as a site lists it, before it is stored.
type ListedChapter struct {
	Name string
	URL  string
}

// Tracked reports whether the series is in the library, saved or followed.
func (v SeriesView) Tracked() bool { return v.Series.ID != 0 }

// ChapterTotal is how many chapters the series has.
func (v SeriesView) ChapterTotal() int {
	if v.Tracked() {
		return v.Counts.Total
	}
	return len(v.Listed)
}

// CoverURL is where the cover is served from, or "" when there is none. A
// series not in the library routes it through the site's preview-cover,
// since image hosts refuse a Referer that is not their own.
func (v SeriesView) CoverURL() string {
	switch {
	case v.Series.CoverURL == "":
		return ""
	case v.Tracked():
		return fmt.Sprintf("/series/%d/cover", v.Series.ID)
	default:
		return fmt.Sprintf("/modules/%s/preview-cover?url=%s", v.Module, escapeQueryValue(v.Series.CoverURL))
	}
}

// Genres splits the comma-separated genre list for display.
func (v SeriesView) Genres() []string {
	var out []string
	for _, g := range strings.Split(v.Series.Genres, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// ToDownload is how many chapters the download button would queue.
func (v SeriesView) ToDownload() int {
	if !v.Tracked() {
		return len(v.Listed)
	}
	return v.Counts.Waiting() + v.Gone()
}

// DownloadLabel names the download button: all of them while none is
// downloaded or on its way, what remains once some are.
func (v SeriesView) DownloadLabel() string {
	c := v.Counts
	if !v.Tracked() || c.Done-v.Gone()+c.Queued+c.Downloading == 0 {
		return fmt.Sprintf("Download all %d", v.ToDownload())
	}
	return fmt.Sprintf("Download %d remaining", v.ToDownload())
}

// StatusLine states where a series in the library stands: what is downloaded
// and on its way, and whether new chapters will be picked up. It ends where
// the Check now link follows for a followed series.
func (v SeriesView) StatusLine() string {
	c := v.Counts
	var parts []string
	if !v.Series.Subscribed {
		parts = append(parts, "In your library")
	}
	// A chapter whose file has gone is not downloaded, whatever the database
	// recorded when it was.
	parts = append(parts, fmt.Sprintf("%d of %d downloaded", c.Done-v.Gone(), c.Total))
	if c.Downloading > 0 {
		parts = append(parts, fmt.Sprintf("%d downloading", c.Downloading))
	}
	if c.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", c.Queued))
	}
	if c.Failed > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", c.Failed))
	}
	if n := v.Gone(); n > 0 {
		parts = append(parts, Count(n, "file", "files")+" missing from disk")
	}
	switch {
	case !v.Series.Subscribed:
		parts = append(parts, "not following")
	case v.CheckInterval <= 0:
		parts = append(parts, "automatic checks are off")
	default:
		parts = append(parts, "new chapters download automatically")
		if v.Series.CheckedAt.Valid {
			parts = append(parts, "checked "+ago(v.Series.CheckedAt.Time))
		} else {
			parts = append(parts, "not checked yet")
		}
	}
	return strings.Join(parts, " · ")
}

// Gone counts the chapters whose file has disappeared.
func (v SeriesView) Gone() int { return len(v.Missing) }

// SettingsURL is where this series' site is configured.
//
// The page links to it because a check can fail for a reason only a setting
// fixes, and the settings were previously reachable only by knowing the module
// name and navigating in from the site chooser.
func (v SeriesView) SettingsURL() string {
	return "/modules/" + v.Series.Key() + "/settings"
}

// ChaptersEmptyReason explains an empty chapter list.
//
// Rendering a failure as an empty page is the worst thing this interface can
// do: the reader cannot tell whether the series has no chapters, the check
// never ran, or it ran and broke.
func (v SeriesView) ChaptersEmptyReason() (headline, detail string) {
	if !v.Series.CheckedAt.Valid {
		return "Not checked yet",
			"Use Check now in the ⋯ menu to fetch the chapter list from " + v.Series.ModuleName + "."
	}
	// A gated site answers normally and simply omits the chapters, so an empty
	// list is the expected symptom of a missing account rather than a puzzle.
	if v.SiteNeedsLogin && !v.SiteHasCredentials {
		return "This site needs an account",
			v.Series.ModuleName + " only lists chapters to signed-in readers. The page was " +
				"fetched successfully and simply contained none. Add a username and password " +
				"in the site's settings, then check again."
	}
	return "No chapters listed",
		"The last check reached " + v.Series.ModuleName + " but it listed no chapters. " +
			"The series may have moved, the module for this site may be out of date, or the " +
			"series may genuinely have nothing published yet."
}

// humanDuration renders an interval the way someone would say it.
func humanDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return plural(int(d/(24*time.Hour)), "day")
	case d >= time.Hour && d%time.Hour == 0:
		return plural(int(d/time.Hour), "hour")
	case d >= time.Minute:
		return plural(int(d/time.Minute), "minute")
	default:
		return d.String()
	}
}

func plural(n int, unit string) string {
	return Count(n, unit, unit+"s")
}
