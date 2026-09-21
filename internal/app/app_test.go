package app

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
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
		res, err := a.Browse(ctx, "TestMadara", BrowsePos{})
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Entries) != 1 || res.Entries[0].Name != "Solo Leveling" {
			t.Fatalf("browse returned %+v", res.Entries)
		}
		// One section, so continuing stays in it.
		if res.Sections != 1 || res.Next != (BrowsePos{Page: 1}) {
			t.Errorf("sections=%d next=%+v", res.Sections, res.Next)
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

// gridImage builds an image whose every tile is a distinct flat colour.
func gridImage(hor, ver, tile int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, hor*tile, ver*tile))
	for i := 0; i < hor*ver; i++ {
		c := color.RGBA{R: uint8(17 * (i + 1)), G: uint8(255 - 13*i), B: uint8(7 * i), A: 255}
		x0, y0 := (i%hor)*tile, (i/hor)*tile
		for y := y0; y < y0+tile; y++ {
			for x := x0; x < x0+tile; x++ {
				img.Set(x, y, c)
			}
		}
	}
	return img
}

// scrambleTiles produces what the site serves: tile i holds what belongs at
// matrix[i], which is the arrangement DeScramble reverses.
func scrambleTiles(src *image.RGBA, hor, ver, tile int, matrix []int) []byte {
	out := image.NewRGBA(src.Bounds())
	for i := 0; i < hor*ver; i++ {
		sx, sy := (matrix[i]%hor)*tile, (matrix[i]/hor)*tile
		dx, dy := (i%hor)*tile, (i/hor)*tile
		for y := 0; y < tile; y++ {
			for x := 0; x < tile; x++ {
				out.Set(dx+x, dy+y, src.At(sx+x, sy+y))
			}
		}
	}
	var buf bytes.Buffer
	_ = png.Encode(&buf, out)
	return buf.Bytes()
}

// TestScrambledChapterEndToEnd proves the image hooks are wired: a site that
// serves a tiled, shuffled page and a module that descrambles it in
// OnDownloadImage must produce a correct image inside the CBZ.
//
// It also checks that the Referer from OnBeforeDownloadImage reaches the
// server, since image hosts commonly 403 without one.
func TestScrambledChapterEndToEnd(t *testing.T) {
	const hor, ver, tile = 3, 3, 12
	matrix := []int{4, 0, 8, 2, 6, 1, 7, 3, 5}
	original := gridImage(hor, ver, tile)
	scrambled := scrambleTiles(original, hor, ver, tile, matrix)

	var gotReferer string
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/puzzle/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<div class="post-title"><h1>Puzzle Series</h1></div>
			<li class="wp-manga-chapter"><a href="%s/manga/puzzle/chapter-1/">Chapter 1</a></li>
		</body></html>`, base)
	})
	mux.HandleFunc("/manga/puzzle/chapter-1/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/p1.png"></div>`, base)
	})
	mux.HandleFunc("/p1.png", func(w http.ResponseWriter, r *http.Request) {
		gotReferer = r.Header.Get("Referer")
		w.Header().Set("Content-Type", "image/png")
		w.Write(scrambled)
	})

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)

	// A module in the shape WolfManga uses: fetch the image itself, then
	// descramble it in place before handing it back.
	checkout := t.TempDir()
	luaDir := filepath.Join(checkout, "lua", "modules")
	if err := os.MkdirAll(luaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	upstream := upstreamLua(t)
	for _, shared := range []string{"templates", "utils"} {
		if err := os.Symlink(filepath.Join(upstream, shared),
			filepath.Join(checkout, "lua", shared)); err != nil {
			t.Fatal(err)
		}
	}
	src := fmt.Sprintf(`
function Init()
	local m = NewWebsiteModule()
	m.ID                    = 'cccccccccccccccccccccccccccccccc'
	m.Name                  = 'PuzzleSite'
	m.RootURL               = '%s'
	m.OnGetInfo             = 'GetInfo'
	m.OnGetPageNumber       = 'GetPageNumber'
	m.OnBeforeDownloadImage = 'BeforeDownloadImage'
	m.OnDownloadImage       = 'DownloadImage'
end

local Template = require 'templates.Madara'
MATRIX = {4, 0, 8, 2, 6, 1, 7, 3, 5}

function GetInfo()       Template.GetInfo() return no_error end
function GetPageNumber() return Template.GetPageNumber() end

function BeforeDownloadImage()
	HTTP.Headers.Values['Referer'] = MODULE.RootURL
	return true
end

function DownloadImage()
	if not HTTP.GET(URL) then return false end
	local puzzle = require 'fmd.imagepuzzle'.Create(3, 3)
	for i = 0, 8 do
		puzzle.Matrix[i] = MATRIX[i + 1]
	end
	puzzle.DeScramble(HTTP.Document, HTTP.Document)
	return true
end
`, srv.URL)
	if err := os.WriteFile(filepath.Join(luaDir, "PuzzleSite.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, LibraryDir: filepath.Join(dir, "library"),
		Workers: 1, HostConcurrency: 4, HostRPS: 1000,
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
	go a.Pool.Run(ctx)

	if err := a.EnqueueRefresh(ctx, "PuzzleSite", srv.URL+"/manga/puzzle/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, err := st.ListSeries(ctx)
		return err == nil && len(s) == 1
	}, "series to be stored")

	all, _ := st.ListSeries(ctx)
	chs, _ := st.ListChapters(ctx, all[0].ID)
	if len(chs) != 1 {
		t.Fatalf("got %d chapters", len(chs))
	}

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
	if gotReferer != srv.URL {
		t.Errorf("Referer = %q, want %q from OnBeforeDownloadImage", gotReferer, srv.URL)
	}

	// The archive must hold the reassembled image, not what the site served.
	zr, err := zip.OpenReader(c.FilePath)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	// The metadata comes first, then the page.
	if len(zr.File) != 2 {
		t.Fatalf("archive holds %d entries", len(zr.File))
	}
	if name := zr.File[0].Name; name != "ComicInfo.xml" {
		t.Errorf("first entry = %q, want the metadata", name)
	}
	if name := zr.File[1].Name; name != "0001.png" {
		t.Errorf("entry name = %q, want 0001.png", name)
	}
	rc, err := zr.File[1].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	stored, _, err := image.Decode(rc)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < ver*tile; y++ {
		for x := 0; x < hor*tile; x++ {
			wr, wg, wb, _ := original.At(x, y).RGBA()
			gr, gg, gb, _ := stored.At(x, y).RGBA()
			if wr != gr || wg != gg || wb != gb {
				t.Fatalf("pixel (%d,%d) differs: the image was not descrambled", x, y)
			}
		}
	}
}
