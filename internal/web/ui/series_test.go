package ui

import (
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/store"
)

func chapters(states ...string) []store.Chapter {
	out := make([]store.Chapter, 0, len(states))
	for i, s := range states {
		out = append(out, store.Chapter{ID: int64(i + 1), State: s})
	}
	return out
}

func TestCountChapters(t *testing.T) {
	c := CountChapters(chapters(
		store.ChapterDone, store.ChapterDone,
		store.ChapterPending, store.ChapterQueued,
		store.ChapterDownloading, store.ChapterFailed,
	))
	if c.Total != 6 || c.Done != 2 || c.Pending != 1 || c.Queued != 1 || c.Downloading != 1 || c.Failed != 1 {
		t.Fatalf("counts = %+v", c)
	}
	// Failed chapters are waiting too: they are what "Download N" would retry.
	if c.Waiting() != 2 {
		t.Errorf("Waiting() = %d, want 2", c.Waiting())
	}
}

// TestStatusLine covers the line under a series' actions: what is
// downloaded and on its way, and whether new chapters will be picked up.
func TestStatusLine(t *testing.T) {
	checked := sql.NullTime{Time: time.Now().Add(-12 * time.Minute), Valid: true}
	cases := []struct {
		name   string
		series store.Series
		every  time.Duration
		states []string
		want   string
	}{
		{"saved", store.Series{ID: 1}, 6 * time.Hour,
			[]string{store.ChapterDone, store.ChapterPending},
			"In your library · 1 of 2 downloaded · not following"},
		{"following, with work in progress", store.Series{ID: 1, Subscribed: true, CheckedAt: checked}, 6 * time.Hour,
			[]string{store.ChapterDone, store.ChapterDownloading, store.ChapterDownloading, store.ChapterQueued, store.ChapterFailed},
			"1 of 5 downloaded · 2 downloading · 1 queued · 1 failed · new chapters download automatically · checked 12m ago"},
		{"following, never checked", store.Series{ID: 1, Subscribed: true}, 6 * time.Hour,
			[]string{store.ChapterPending},
			"0 of 1 downloaded · new chapters download automatically · not checked yet"},
		// Following with checks switched off would be a promise the app is not
		// keeping.
		{"following, checks off", store.Series{ID: 1, Subscribed: true, CheckedAt: checked}, 0,
			[]string{store.ChapterDone},
			"1 of 1 downloaded · automatic checks are off"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := SeriesView{Series: c.series, CheckInterval: c.every, Chapters: chapters(c.states...)}
			v.Counts = CountChapters(v.Chapters)
			if got := v.StatusLine(); got != c.want {
				t.Errorf("got  %q\nwant %q", got, c.want)
			}
		})
	}
}

// TestDownloadLabel covers the download button: all of them until something
// is downloaded or on its way, then what remains.
func TestDownloadLabel(t *testing.T) {
	untracked := SeriesView{Listed: make([]ListedChapter, 124)}
	if got := untracked.DownloadLabel(); got != "Download all 124" {
		t.Errorf("untracked: %q", got)
	}
	v := SeriesView{Series: store.Series{ID: 1}, Chapters: chapters(store.ChapterPending, store.ChapterPending)}
	v.Counts = CountChapters(v.Chapters)
	if got := v.DownloadLabel(); got != "Download all 2" {
		t.Errorf("nothing downloaded: %q", got)
	}
	v.Chapters = chapters(store.ChapterDone, store.ChapterQueued, store.ChapterPending, store.ChapterFailed)
	v.Counts = CountChapters(v.Chapters)
	if got := v.DownloadLabel(); got != "Download 2 remaining" {
		t.Errorf("some downloaded: %q", got)
	}
}

// TestChaptersEmptyReason is the fix for the worst thing the old page did:
// showing nothing, with no way to tell an unchecked series from a broken one.
func TestChaptersEmptyReason(t *testing.T) {
	never := SeriesView{Series: store.Series{ModuleName: "AzoraManga"}}
	head, detail := never.ChaptersEmptyReason()
	if !strings.Contains(head, "Not checked") {
		t.Errorf("headline = %q", head)
	}
	if !strings.Contains(detail, "AzoraManga") {
		t.Errorf("detail should name the site, got %q", detail)
	}

	checked := SeriesView{Series: store.Series{
		ModuleName: "AzoraManga",
		CheckedAt:  sql.NullTime{Time: time.Now(), Valid: true},
	}}
	head, detail = checked.ChaptersEmptyReason()
	if !strings.Contains(head, "No chapters listed") {
		t.Errorf("headline = %q", head)
	}
	if !strings.Contains(detail, "out of date") {
		t.Errorf("detail should suggest a cause, got %q", detail)
	}
}

func TestHumanDuration(t *testing.T) {
	cases := map[time.Duration]string{
		6 * time.Hour:    "6 hours",
		time.Hour:        "1 hour",
		30 * time.Minute: "30 minutes",
		24 * time.Hour:   "1 day",
		48 * time.Hour:   "2 days",
	}
	for d, want := range cases {
		if got := humanDuration(d); got != want {
			t.Errorf("humanDuration(%s) = %q, want %q", d, got, want)
		}
	}
}
