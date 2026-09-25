package app

import (
	"sync/atomic"
	"testing"

	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/store"
)

// TestDownloadChapterOfSavesTheSeries covers downloading from a series that
// is not in the library: it is added as saved, not followed, with its whole
// chapter list, and only the chosen chapter is downloaded.
func TestDownloadChapterOfSavesTheSeries(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(3)
	srv := growingSite(t, &chapters)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{AutoDownload: true})
	seriesURL := srv.URL + "/manga/grow/"

	id, err := a.DownloadChapterOf(ctx, "TestMadara", seriesURL, srv.URL+"/manga/grow/chapter-2/")
	if err != nil {
		t.Fatal(err)
	}
	series, err := st.GetSeries(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if series.Subscribed {
		t.Error("downloading a chapter should save the series, not follow it")
	}
	if series.Title != "Growing Series" {
		t.Errorf("title %q, want the one the site gives", series.Title)
	}
	stateOf := func(name string) string {
		chs, _ := st.ListChapters(ctx, id)
		for _, c := range chs {
			if c.Name == name {
				return c.State
			}
		}
		return "absent"
	}
	waitFor(t, ctx, func() bool { return stateOf("Chapter 2") == store.ChapterDone }, "chapter 2 to download")
	if chs, _ := st.ListChapters(ctx, id); len(chs) != 3 {
		t.Errorf("stored %d chapters, want the whole list of 3", len(chs))
	}
	for _, name := range []string{"Chapter 1", "Chapter 3"} {
		if got := stateOf(name); got != store.ChapterPending {
			t.Errorf("%s is %q; only the chosen chapter should download", name, got)
		}
	}

	// A second chapter joins the same series.
	again, err := a.DownloadChapterOf(ctx, "TestMadara", seriesURL, srv.URL+"/manga/grow/chapter-1/")
	if err != nil || again != id {
		t.Fatalf("second chapter: series %d, %v; want %d", again, err, id)
	}
	waitFor(t, ctx, func() bool { return stateOf("Chapter 1") == store.ChapterDone }, "chapter 1 to download")
	if all, _ := st.ListSeries(ctx); len(all) != 1 {
		t.Errorf("%d series in the library, want 1", len(all))
	}

	// Following a saved series turns following on for that same series.
	followed, err := a.Follow(ctx, "TestMadara", seriesURL, "")
	if err != nil || followed != id {
		t.Fatalf("follow: series %d, %v; want %d", followed, err, id)
	}
	if series, _ := st.GetSeries(ctx, id); !series.Subscribed {
		t.Error("following a saved series should turn following on")
	}
}

// TestDownloadChapterOfLeavesNothingWhenItFails covers a download that cannot
// start: the series it would have added is not left behind in the library.
func TestDownloadChapterOfLeavesNothingWhenItFails(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(3)
	srv := growingSite(t, &chapters)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	for name, c := range map[string]struct{ series, chapter string }{
		"the site does not answer": {srv.URL + "/manga/missing/", srv.URL + "/manga/missing/chapter-1/"},
		"the chapter is not on it": {srv.URL + "/manga/grow/", srv.URL + "/manga/grow/chapter-9/"},
	} {
		if _, err := a.DownloadChapterOf(ctx, "TestMadara", c.series, c.chapter); err == nil {
			t.Errorf("%s: want an error", name)
		}
		if all, _ := st.ListSeries(ctx); len(all) != 0 {
			t.Errorf("%s: %d series left in the library", name, len(all))
		}
	}
}
