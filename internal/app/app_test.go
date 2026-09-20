package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// upstreamLua locates an FMD2 checkout. Its modules are GPL-2.0-only and are
// never vendored here, so the test skips when none is available.
func upstreamLua(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("ATSUME_FMD2_DIR")
	if dir == "" {
		dir = filepath.Join("..", "txquery", "testdata", "fmd2")
	}
	lua, err := filepath.Abs(filepath.Join(dir, "lua"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lua); err != nil {
		t.Skip("no FMD2 checkout; set ATSUME_FMD2_DIR to point at one")
	}
	return lua
}

// fakeCheckout builds a module tree that reuses upstream's shared templates but
// supplies our own module, without writing anything into the upstream copy.
func fakeCheckout(t *testing.T, rootURL string) string {
	t.Helper()
	upstream := upstreamLua(t)
	dir := t.TempDir()
	luaDir := filepath.Join(dir, "lua")
	if err := os.MkdirAll(filepath.Join(luaDir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []string{"templates", "utils"} {
		if err := os.Symlink(filepath.Join(upstream, shared), filepath.Join(luaDir, shared)); err != nil {
			t.Fatal(err)
		}
	}

	src := fmt.Sprintf(`
function Init()
	local m = NewWebsiteModule()
	m.ID               = '46e0c618a19748d6af150c2f198f5360'
	m.Name             = 'TestMadara'
	m.RootURL          = '%s'
	m.Category         = 'English'
	m.OnGetNameAndLink = 'GetNameAndLink'
	m.OnGetInfo        = 'GetInfo'
	m.OnGetPageNumber  = 'GetPageNumber'
end

local Template = require 'templates.Madara'

function GetNameAndLink() Template.GetNameAndLink() return no_error end
function GetInfo()        Template.GetInfo()        return no_error end
function GetPageNumber()  return Template.GetPageNumber() end
`, rootURL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "TestMadara.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// site serves a Madara-shaped manga plus its images.
func site(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/wp-admin/admin-ajax.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<div class="post-title"><h3><a href="%s/manga/solo-leveling/">Solo Leveling</a></h3></div>`, base)
	})
	mux.HandleFunc("/manga/solo-leveling/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<div class="post-title"><h1>Solo Leveling</h1></div>
			<div class="summary_image"><img data-src="%s/cover.jpg"></div>
			<div class="author-content"><a>Chugong</a></div>
			<div class="genres-content"><a>Action</a></div>
			<div class="summary__content"><p>Ten years ago.</p></div>
			<li class="wp-manga-chapter"><a href="%s/manga/solo-leveling/chapter-2/">Chapter 2</a></li>
			<li class="wp-manga-chapter"><a href="%s/manga/solo-leveling/chapter-1/">Chapter 1</a></li>
		</body></html>`, base, base, base)
	})
	for _, ch := range []string{"chapter-1", "chapter-2"} {
		mux.HandleFunc("/manga/solo-leveling/"+ch+"/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/p1.jpg"></div>
				<div class="page-break"><img data-src="%s/p2.jpg"></div>`, base, base)
		})
	}
	mux.HandleFunc("/p1.jpg", serveImage)
	mux.HandleFunc("/p2.jpg", serveImage)

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

func serveImage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/jpeg")
	w.Write([]byte("\xff\xd8\xff\xe0 fake jpeg bytes"))
}

// TestFullSlice runs the whole path a user drives from the UI: browse a site,
// track a title, refresh its chapters, download one, and end up with a CBZ.
func TestFullSlice(t *testing.T) {
	srv := site(t)
	checkout := fakeCheckout(t, srv.URL)

	dir := t.TempDir()
	cfg := &config.Config{
		DataDir:         dir,
		LibraryDir:      filepath.Join(dir, "library"),
		Workers:         1,
		HostConcurrency: 4,
		// The limiter is real; a realistic rate here would just make the test crawl.
		HostRPS: 1000,
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	reg := scraper.NewRegistry(filepath.Join(dir, "modules"), "")
	if err := reg.Use(checkout, "test"); err != nil {
		t.Fatal(err)
	}

	a := New(cfg, st, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, unsubscribe := a.Bus.Subscribe()
	defer unsubscribe()
	go a.Pool.Run(ctx)

	t.Run("Browse", func(t *testing.T) {
		entries, err := a.Browse(ctx, "TestMadara", 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 1 || entries[0].Name != "Solo Leveling" {
			t.Fatalf("browse returned %+v", entries)
		}
	})

	t.Run("TrackAndRefresh", func(t *testing.T) {
		if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/solo-leveling/"); err != nil {
			t.Fatal(err)
		}
		waitFor(t, ctx, func() bool {
			s, err := st.ListSeries(ctx)
			return err == nil && len(s) == 1
		}, "series to be stored")

		all, err := st.ListSeries(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if all[0].Title != "Solo Leveling" || all[0].Authors != "Chugong" {
			t.Errorf("series = %+v", all[0])
		}

		chs, err := st.ListChapters(ctx, all[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(chs) != 2 {
			t.Fatalf("got %d chapters, want 2", len(chs))
		}
		// Madara reverses, so position 0 is the oldest chapter.
		if chs[0].Name != "Chapter 1" || chs[0].Number != "001" {
			t.Errorf("first chapter = %+v", chs[0])
		}
	})

	t.Run("Download", func(t *testing.T) {
		all, _ := st.ListSeries(ctx)
		chs, _ := st.ListChapters(ctx, all[0].ID)

		if err := a.EnqueueDownload(ctx, chs[0].ID); err != nil {
			t.Fatal(err)
		}
		waitFor(t, ctx, func() bool {
			c, err := st.GetChapter(ctx, chs[0].ID)
			return err == nil && (c.State == store.ChapterDone || c.State == store.ChapterFailed)
		}, "chapter to finish")

		c, err := st.GetChapter(ctx, chs[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if c.State != store.ChapterDone {
			t.Fatalf("state = %s, error = %s", c.State, c.Error)
		}
		if c.Pages != 2 {
			t.Errorf("pages = %d, want 2", c.Pages)
		}

		want := filepath.Join(cfg.LibraryDir, "Solo Leveling", "Solo Leveling - c001.cbz")
		if c.FilePath != want {
			t.Errorf("file path = %q, want %q", c.FilePath, want)
		}
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("CBZ not on disk: %v", err)
		}
	})

	t.Run("ProgressEventsPublished", func(t *testing.T) {
		var kinds []string
	drain:
		for {
			select {
			case e := <-events:
				kinds = append(kinds, e.Kind)
			default:
				break drain
			}
		}
		joined := strings.Join(kinds, ",")
		for _, want := range []string{"series-updated", "chapter-progress", "chapter-updated"} {
			if !strings.Contains(joined, want) {
				t.Errorf("no %s event was published; saw %v", want, kinds)
			}
		}
	})
}

// waitFor polls until cond holds or the test times out.
func waitFor(t *testing.T, ctx context.Context, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("context cancelled waiting for %s", what)
		case <-time.After(50 * time.Millisecond):
		}
	}
	t.Fatalf("timed out waiting for %s", what)
}
