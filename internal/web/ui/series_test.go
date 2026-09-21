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

func TestHaveLine(t *testing.T) {
	cases := []struct {
		name   string
		states []string
		want   string
	}{
		{"nothing known", nil, ""},
		{"all downloaded", []string{store.ChapterDone}, "1 of 1 chapters downloaded"},
		{
			name:   "in progress and failed are called out",
			states: []string{store.ChapterDone, store.ChapterDownloading, store.ChapterQueued, store.ChapterFailed},
			want:   "1 of 4 chapters downloaded · 2 in progress · 1 failed",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := SeriesView{Chapters: chapters(c.states...)}
			v.Counts = CountChapters(v.Chapters)
			if got := v.HaveLine(); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

// TestFollowLine covers the sentence that has to carry the whole contract:
// what following does, and how often.
func TestFollowLine(t *testing.T) {
	following := store.Series{Subscribed: true}

	v := SeriesView{Series: following, CheckInterval: 6 * time.Hour}
	if got := v.FollowLine(); !strings.Contains(got, "every 6 hours") || !strings.Contains(got, "automatically") {
		t.Errorf("following line = %q; it must say how often and that it downloads", got)
	}

	v = SeriesView{Series: following, CheckInterval: time.Hour}
	if got := v.FollowLine(); !strings.Contains(got, "every 1 hour") {
		t.Errorf("singular interval = %q", got)
	}

	// Automatic checks switched off entirely — following on its own would be a
	// promise the app is not keeping.
	v = SeriesView{Series: following, CheckInterval: 0}
	if got := v.FollowLine(); !strings.Contains(got, "switched off") {
		t.Errorf("disabled line = %q", got)
	}

	v = SeriesView{Series: store.Series{Subscribed: false}, CheckInterval: 6 * time.Hour}
	if got := v.FollowLine(); !strings.Contains(got, "Not following") {
		t.Errorf("unfollowed line = %q", got)
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
