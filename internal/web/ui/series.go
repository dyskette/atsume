package ui

import (
	"fmt"
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
type SeriesView struct {
	Series   store.Series
	Chapters []store.Chapter
	Counts   ChapterCounts
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

// HaveLine states what the reader has, in the terms they care about.
func (v SeriesView) HaveLine() string {
	c := v.Counts
	if c.Total == 0 {
		return ""
	}
	// A chapter whose file has gone is not downloaded, whatever the database
	// recorded when it was.
	out := fmt.Sprintf("%d of %d chapters downloaded", c.Done-v.Gone(), c.Total)
	if n := v.Gone(); n > 0 {
		out += fmt.Sprintf(" · %s missing from disk", Count(n, "file", "files"))
	}
	if n := c.Downloading + c.Queued; n > 0 {
		out += fmt.Sprintf(" · %d in progress", n)
	}
	if c.Failed > 0 {
		out += fmt.Sprintf(" · %d failed", c.Failed)
	}
	return out
}

// FollowLine states the standing arrangement in full, because "following" on
// its own does not say what it will do or how often.
func (v SeriesView) FollowLine() string {
	if !v.Series.Subscribed {
		return "Not following. New chapters will not be picked up."
	}
	if v.CheckInterval <= 0 {
		return "Following, but automatic checks are switched off. Use Check now."
	}
	return fmt.Sprintf("Following. New chapters are checked for every %s and downloaded automatically.",
		humanDuration(v.CheckInterval))
}

// ChaptersEmptyReason explains an empty chapter list.
//
// Rendering a failure as an empty page is the worst thing this interface can
// do: the reader cannot tell whether the series has no chapters, the check
// never ran, or it ran and broke.
func (v SeriesView) ChaptersEmptyReason() (headline, detail string) {
	if !v.Series.CheckedAt.Valid {
		return "Not checked yet",
			"Press “Check for new chapters” to fetch the chapter list from " + v.Series.ModuleName + "."
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
