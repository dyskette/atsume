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
	// CheckInterval is how often a followed series is re-checked. Zero means
	// automatic checking is switched off entirely.
	CheckInterval time.Duration
}

// HaveLine states what the reader has, in the terms they care about.
func (v SeriesView) HaveLine() string {
	c := v.Counts
	if c.Total == 0 {
		return ""
	}
	out := fmt.Sprintf("%d of %d chapters downloaded", c.Done, c.Total)
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
	return "No chapters listed",
		"The last check reached " + v.Series.ModuleName + " but it listed no chapters. " +
			"The series may have moved, or the module for this site may be out of date."
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
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}
