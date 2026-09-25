package ui

import (
	"bytes"
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/store"
)

func renderPage(t *testing.T, v SeriesView) string {
	t.Helper()
	var buf bytes.Buffer
	if err := SeriesPage(v).Render(context.Background(), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

func mustContain(t *testing.T, page string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(page, w) {
			t.Errorf("page is missing %q", w)
		}
	}
}

func mustNotContain(t *testing.T, page string, unwanted ...string) {
	t.Helper()
	for _, w := range unwanted {
		if strings.Contains(page, w) {
			t.Errorf("page should not contain %q", w)
		}
	}
}

// TestSeriesPageStates covers the one series page in each of its states: the
// same page, with its controls following the series from untracked to saved
// to followed.
func TestSeriesPageStates(t *testing.T) {
	base := store.Series{
		Title: "From Goblin to Goblin God", ModuleName: "MangaRead", ModuleKey: "MangaRead",
		Status: "Ongoing", Genres: "Action, Drama", Summary: "Lin Tian reincarnated as a goblin.",
	}

	t.Run("untracked", func(t *testing.T) {
		page := renderPage(t, SeriesView{
			Series: base, Module: "MangaRead", SeriesURL: "/manga/goblin/",
			Listed: []ListedChapter{{"Chapter 2", "/manga/goblin/2/"}, {"Chapter 1", "/manga/goblin/1/"}},
		})
		mustContain(t, page,
			`hx-post="/modules/MangaRead/series/follow"`, "+ Follow",
			"↓ Download all 2",
			"Follow to get new chapters automatically, or download any chapter to keep it.",
			`value="/manga/goblin/1/"`, "Chapters <span class=\"count\">2</span>",
			"<li>Action</li>", "Site settings",
		)
		mustNotContain(t, page, "In library", "Remove from library", "Check now")
	})

	saved := base
	saved.ID = 7
	chs := []store.Chapter{
		{ID: 1, Name: "Chapter 3", State: store.ChapterPending},
		{ID: 2, Name: "Chapter 2", State: store.ChapterPending},
		{ID: 3, Name: "Chapter 1", State: store.ChapterDone},
	}

	t.Run("saved", func(t *testing.T) {
		v := SeriesView{Series: saved, Chapters: chs, Counts: CountChapters(chs), CheckInterval: 6 * time.Hour}
		page := renderPage(t, v)
		mustContain(t, page,
			"✓ In library",
			`hx-post="/series/7/subscribe"`, `name="on" value="1"`, "+ Follow",
			"↓ Download 2 remaining",
			"In your library · 1 of 3 downloaded · not following",
			"Remove from library", `hx-post="/series/7/refresh"`,
		)
		mustNotContain(t, page, "✓ Following", "Follow to get new chapters automatically")
	})

	t.Run("following", func(t *testing.T) {
		following := saved
		following.Subscribed = true
		following.CheckedAt = sql.NullTime{Time: time.Now().Add(-12 * time.Minute), Valid: true}
		done := []store.Chapter{{ID: 3, Name: "Chapter 1", State: store.ChapterDone}}
		page := renderPage(t, SeriesView{Series: following, Chapters: done, Counts: CountChapters(done), CheckInterval: 6 * time.Hour})
		mustContain(t, page,
			"✓ Following", `name="on" value="0"`,
			`class="status on"`, "1 of 1 downloaded · new chapters download automatically · checked 12m ago",
		)
		// Nothing left to download, so no download button.
		mustNotContain(t, page, "↓ Download")
	})
}
