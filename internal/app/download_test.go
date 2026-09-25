package app

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// slowSite serves a series whose downloads stop until the test lets them go:
// chapter 1 hangs on its second page, chapter 2 on its page list, and
// chapter 3 downloads at once. A request cut off by a cancelled download
// returns as soon as its client gives up.
func slowSite(t *testing.T) (*httptest.Server, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	var base string
	hold := func(r *http.Request) bool {
		select {
		case <-release:
			return true
		case <-r.Context().Done():
			return false
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/slow/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><body><div class="post-title"><h1>Slow Series</h1></div>`)
		for i := 3; i >= 1; i-- {
			fmt.Fprintf(w, `<li class="wp-manga-chapter"><a href="%s/manga/slow/chapter-%d/">Chapter %d</a></li>`, base, i, i)
		}
		fmt.Fprint(w, `</body></html>`)
	})
	mux.HandleFunc("/manga/slow/chapter-1/", func(w http.ResponseWriter, r *http.Request) {
		for p := 1; p <= 3; p++ {
			fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/c1/p%d.jpg"></div>`, base, p)
		}
	})
	mux.HandleFunc("/manga/slow/chapter-2/", func(w http.ResponseWriter, r *http.Request) {
		if hold(r) {
			fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/c1/p1.jpg"></div>`, base)
		}
	})
	mux.HandleFunc("/manga/slow/chapter-3/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/c1/p1.jpg"></div>`, base)
	})
	mux.HandleFunc("/c1/", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "p2.jpg") && !hold(r) {
			return
		}
		serveImage(w, r)
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv, release
}

// TestCancelRunningDownload covers stopping a chapter mid-download: while its
// pages come in and while its page list is still being fetched. Either way
// the chapter goes back to not downloaded, nothing reaches the library, and
// the job is not retried.
func TestCancelRunningDownload(t *testing.T) {
	srv, release := slowSite(t)
	defer close(release)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{Workers: 2})
	seriesURL := srv.URL + "/manga/slow/"

	start := func(n int) int64 {
		t.Helper()
		id, err := a.DownloadChapterOf(ctx, "TestMadara", seriesURL, fmt.Sprintf("%s/manga/slow/chapter-%d/", srv.URL, n))
		if err != nil {
			t.Fatal(err)
		}
		chID, err := st.ChapterIDByURL(ctx, id, fmt.Sprintf("%s/manga/slow/chapter-%d/", srv.URL, n))
		if err != nil || chID == 0 {
			t.Fatalf("chapter %d: %d, %v", n, chID, err)
		}
		return chID
	}
	ch1, ch2 := start(1), start(2)

	// Chapter 1 holds on its second page: one of three in. Chapter 2 holds
	// on its page list, so its page count is not known yet.
	waitFor(t, ctx, func() bool { return a.ActiveDownloads()[ch1] == DownloadProgress{Done: 1, Total: 3} }, "chapter 1 at page 1 of 3")
	waitFor(t, ctx, func() bool {
		p, ok := a.ActiveDownloads()[ch2]
		return ok && p.Total == 0
	}, "chapter 2 fetching its page list")

	for _, id := range []int64{ch1, ch2} {
		if err := a.CancelChapter(ctx, id); err != nil {
			t.Fatalf("cancel %d: %v", id, err)
		}
	}
	for _, id := range []int64{ch1, ch2} {
		waitFor(t, ctx, func() bool {
			c, err := st.GetChapter(ctx, id)
			_, running := a.ActiveDownloads()[id]
			return err == nil && c.State == store.ChapterPending && !running
		}, "a cancelled chapter to be not downloaded")
	}

	if files, _ := filepath.Glob(filepath.Join(a.Cfg.LibraryDir, "*", "*.cbz")); len(files) != 0 {
		t.Errorf("a cancelled download wrote %q", files)
	}
	stats, err := a.Queue.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats["pending"] != 0 || stats["failed"] != 0 {
		t.Errorf("jobs after cancelling: %v; a cancel should neither retry nor fail", stats)
	}
}

// TestCancelQueuedChapter covers the queue while paused: places in line, and
// taking a chapter out of it.
func TestCancelQueuedChapter(t *testing.T) {
	srv, release := slowSite(t)
	close(release) // nothing holds here
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})
	seriesURL := srv.URL + "/manga/slow/"

	a.PauseQueue()
	var ids []int64
	for _, n := range []int{3, 1, 2} {
		url := fmt.Sprintf("%s/manga/slow/chapter-%d/", srv.URL, n)
		seriesID, err := a.DownloadChapterOf(ctx, "TestMadara", seriesURL, url)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := st.ChapterIDByURL(ctx, seriesID, url)
		ids = append(ids, id)
	}
	positions := func() map[int64]int {
		p, err := a.QueuePositions(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := positions(); p[ids[0]] != 1 || p[ids[1]] != 2 || p[ids[2]] != 3 {
		t.Errorf("places in line %v, want the order they were queued in", p)
	}

	if err := a.CancelChapter(ctx, ids[1]); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.GetChapter(ctx, ids[1]); c.State != store.ChapterPending {
		t.Errorf("cancelled chapter is %q, want not downloaded", c.State)
	}
	if p := positions(); p[ids[0]] != 1 || p[ids[2]] != 2 || p[ids[1]] != 0 {
		t.Errorf("after cancelling: %v, want the rest moved up", p)
	}
	if err := a.CancelChapter(ctx, ids[1]); err == nil {
		t.Error("cancelling a chapter that is not queued should say so")
	}

	a.ResumeQueue()
	for _, id := range []int64{ids[0], ids[2]} {
		waitFor(t, ctx, func() bool {
			c, _ := st.GetChapter(ctx, id)
			return c.State == store.ChapterDone
		}, "the remaining chapters to download after resuming")
	}
	if c, _ := st.GetChapter(ctx, ids[1]); c.State != store.ChapterPending {
		t.Errorf("the cancelled chapter is %q after resuming", c.State)
	}
}
