package ui

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/store"
)

// goblin is a followed series with n chapters listed oldest first, as most
// sites return them.
func goblin(n int) SeriesView {
	v := SeriesView{Series: store.Series{
		ID: 7, Title: "Goblin", ModuleName: "MangaRead", Subscribed: true,
		CheckedAt: sql.NullTime{Time: time.Now(), Valid: true},
	}}
	for i := 1; i <= n; i++ {
		v.Chapters = append(v.Chapters, store.Chapter{
			ID: int64(i), Name: fmt.Sprintf("Chapter %d", i), Number: fmt.Sprint(i), State: store.ChapterPending,
		})
	}
	v.Counts = CountChapters(v.Chapters)
	return v
}

func names(chs []store.Chapter) []string {
	var out []string
	for _, c := range chs {
		out = append(out, c.Name)
	}
	return out
}

// TestChapterListView covers what the list shows: newest first by default,
// twelve rows until "Show all", and filters that count on-disk chapters only.
func TestChapterListView(t *testing.T) {
	v := goblin(20)
	v.Chapters[0].State = store.ChapterDone
	v.Chapters[1].State = store.ChapterDone
	v.Missing = map[int64]bool{2: true} // its file is gone, so not downloaded
	v.Counts = CountChapters(v.Chapters)

	if got := names(v.VisibleChapters()); len(got) != 12 || got[0] != "Chapter 20" || got[11] != "Chapter 9" {
		t.Errorf("newest first, limited to 12: got %v", got)
	}
	if v.Hidden() != 8 {
		t.Errorf("hidden = %d, want 8", v.Hidden())
	}
	v.Oldest, v.ShowAll = true, true
	if got := names(v.VisibleChapters()); len(got) != 20 || got[0] != "Chapter 1" {
		t.Errorf("oldest first, all: got %v", got)
	}
	if v.DownloadedCount() != 1 || v.NotDownloadedCount() != 19 {
		t.Errorf("counts = %d / %d, want 1 / 19", v.DownloadedCount(), v.NotDownloadedCount())
	}
	v.Filter = FilterDownloaded
	if got := names(v.VisibleChapters()); len(got) != 1 || got[0] != "Chapter 1" {
		t.Errorf("downloaded filter: got %v", got)
	}
	if first, _, _ := v.FirstToDownload(); first.Name != "Chapter 2" {
		t.Errorf("first to download = %q, want Chapter 2", first.Name)
	}
	if got := v.ListURL(FilterNotDownloaded, true, false); got != "/series/7?filter=not-downloaded&sort=oldest" {
		t.Errorf("list URL = %q", got)
	}
}

// TestChapterRow covers a row in each state: one status and one action.
func TestChapterRow(t *testing.T) {
	render := func(c store.Chapter, st RowState) string {
		var buf bytes.Buffer
		if err := ChapterRow(c, false, st).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}
	c := store.Chapter{ID: 3, Name: "Chapter 3"}

	c.State = store.ChapterPending
	row := render(c, RowState{})
	mustContain(t, row, `hx-post="/chapters/3/download"`, "↓")
	mustNotContain(t, row, ">pending<", `class="badge`)

	c.State = store.ChapterQueued
	mustContain(t, render(c, RowState{Position: 1}), "Queued · up next", `hx-post="/chapters/3/cancel"`)
	mustContain(t, render(c, RowState{Position: 2}), "Queued · #2")
	mustContain(t, render(c, RowState{Position: 11}), ">Queued<")

	c.State = store.ChapterDownloading
	mustContain(t, render(c, RowState{Active: true}), "Fetching page list…", `value="0"`)
	mustContain(t, render(c, RowState{Active: true, Done: 14, Total: 35}), "14 / 35", `max="35"`, "✕")

	c.State, c.Pages = store.ChapterDone, 46
	mustContain(t, render(c, RowState{}), "✓ 46 pages", "⋯", "Download again")
}

// TestChapterListEmpty covers the three empty states.
func TestChapterListEmpty(t *testing.T) {
	mustContain(t, renderPage(t, goblin(0)), "No chapters on MangaRead yet", "Check again")

	v := goblin(3)
	v.Filter = FilterDownloaded
	mustContain(t, renderPage(t, v), "No chapters downloaded yet", "Download all 3", "Start with Chapter 1")

	for i := range v.Chapters {
		v.Chapters[i].State = store.ChapterDone
	}
	v.Counts = CountChapters(v.Chapters)
	v.Filter = FilterNotDownloaded
	mustContain(t, renderPage(t, v), "You’re all caught up", "All 3 chapters are downloaded.", "Check now")
}
