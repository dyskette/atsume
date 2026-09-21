package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// growingSite serves a chapter list whose length the test controls, so a second
// check sees chapters the first one did not.
func growingSite(t *testing.T, chapters *atomic.Int32) *httptest.Server {
	t.Helper()
	var base string
	mux := http.NewServeMux()

	mux.HandleFunc("/manga/grow/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<div class="post-title"><h1>Growing Series</h1></div>
			<div class="author-content"><a>Someone</a></div>`)
		// Newest first, the order sites actually use.
		for i := int(chapters.Load()); i >= 1; i-- {
			fmt.Fprintf(w, `<li class="wp-manga-chapter"><a href="%s/manga/grow/chapter-%d/">Chapter %d</a></li>`, base, i, i)
		}
		fmt.Fprint(w, `</body></html>`)
	})
	for i := 1; i <= 10; i++ {
		mux.HandleFunc(fmt.Sprintf("/manga/grow/chapter-%d/", i), func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprintf(w, `<div class="page-break"><img data-src="%s/p1.jpg"></div>`, base)
		})
	}
	mux.HandleFunc("/p1.jpg", serveImage)

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// newTestApp wires an App against a temporary store and a module checkout.
func newTestApp(t *testing.T, rootURL string, cfg *config.Config) (*App, *store.Store, context.Context) {
	t.Helper()
	checkout := fakeCheckout(t, rootURL)
	dir := t.TempDir()

	cfg.DataDir = dir
	cfg.LibraryDir = filepath.Join(dir, "library")
	if cfg.Workers == 0 {
		cfg.Workers = 1
	}
	if cfg.HostConcurrency == 0 {
		cfg.HostConcurrency = 4
	}
	if cfg.HostRPS == 0 {
		cfg.HostRPS = 1000
	}
	if cfg.CheckBatch == 0 {
		cfg.CheckBatch = 10
	}

	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Registered first so it runs last: cleanups unwind in reverse.
	t.Cleanup(func() { st.Close() })

	reg := scraper.NewRegistry(filepath.Join(dir, "modules"), "")
	if err := reg.Use(checkout, "test"); err != nil {
		t.Fatal(err)
	}

	a := New(cfg, st, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)

	// Wait for the pool to unwind before the store closes, so a worker mid-loop
	// does not log against a closed database.
	done := make(chan struct{})
	go func() { a.Pool.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	})
	return a, st, ctx
}

// TestInitialImportDoesNotAutoDownload covers the first look at a series: every
// chapter is unknown, but queuing an entire backlog is not what tracking a
// series is for.
func TestInitialImportDoesNotAutoDownload(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(3)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{AutoDownload: true})

	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, err := st.ListSeries(ctx)
		return err == nil && len(s) == 1
	}, "series to be stored")

	all, _ := st.ListSeries(ctx)
	chs, _ := st.ListChapters(ctx, all[0].ID)
	if len(chs) != 3 {
		t.Fatalf("got %d chapters, want 3", len(chs))
	}
	// Give any erroneously queued download a chance to start before asserting.
	time.Sleep(300 * time.Millisecond)
	for _, c := range chs {
		if c.State != store.ChapterPending {
			t.Errorf("chapter %q is %q; the initial import must not download", c.Name, c.State)
		}
	}
}

// TestNewChaptersAreDownloaded covers the point of a subscription: a later
// check finds chapters the first did not, and only those are queued.
func TestNewChaptersAreDownloaded(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(2)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{AutoDownload: true})

	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, _ := st.ListSeries(ctx)
		if len(s) != 1 {
			return false
		}
		chs, _ := st.ListChapters(ctx, s[0].ID)
		return len(chs) == 2
	}, "the first check to record two chapters")

	all, _ := st.ListSeries(ctx)
	seriesID := all[0].ID

	// The site publishes a third chapter, and the series is checked again.
	chapters.Store(3)
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, seriesID)
		return len(chs) == 3
	}, "the second check to find the new chapter")

	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, seriesID)
		for _, c := range chs {
			if c.Name == "Chapter 3" && c.State == store.ChapterDone {
				return true
			}
		}
		return false
	}, "the new chapter to download")

	chs, _ := st.ListChapters(ctx, seriesID)
	for _, c := range chs {
		switch c.Name {
		case "Chapter 3":
			if c.State != store.ChapterDone {
				t.Errorf("new chapter is %q, want done", c.State)
			}
		default:
			// The ones already known must be left alone, not re-fetched.
			if c.State != store.ChapterPending {
				t.Errorf("pre-existing chapter %q is %q, want pending", c.Name, c.State)
			}
		}
	}
}

// TestAutoDownloadOff records new chapters without queuing them.
func TestAutoDownloadOff(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{AutoDownload: false})

	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, _ := st.ListSeries(ctx)
		return len(s) == 1
	}, "series to be stored")

	all, _ := st.ListSeries(ctx)
	chapters.Store(2)
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, all[0].ID)
		return len(chs) == 2
	}, "the new chapter to be recorded")

	time.Sleep(300 * time.Millisecond)
	chs, _ := st.ListChapters(ctx, all[0].ID)
	for _, c := range chs {
		if c.State != store.ChapterPending {
			t.Errorf("chapter %q is %q; nothing should download with AutoDownload off", c.Name, c.State)
		}
	}
}

// TestSeriesDueForCheck covers the query the scheduler sweeps with.
func TestSeriesDueForCheck(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()

	id, err := st.UpsertSeries(ctx, store.Series{
		ModuleID: "m", ModuleName: "M", URL: "https://x/1", Title: "One", Subscribed: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	// Just checked, so nothing is due against a long interval.
	due, err := st.SeriesDueForCheck(ctx, time.Hour, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("got %d due, want 0", len(due))
	}

	// Against a zero interval everything is overdue.
	if due, err = st.SeriesDueForCheck(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	if len(due) != 1 {
		t.Fatalf("got %d due, want 1", len(due))
	}

	// An unsubscribed series is never swept, however overdue.
	if err := st.SetSubscribed(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if due, err = st.SeriesDueForCheck(ctx, 0, 10); err != nil {
		t.Fatal(err)
	}
	if len(due) != 0 {
		t.Errorf("an unsubscribed series was returned as due")
	}
}

// TestSchedulerSweepEnqueues covers CheckNow driving a sweep into the queue.
func TestSchedulerSweepEnqueues(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{CheckInterval: time.Hour, CheckBatch: 5})

	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, _ := st.ListSeries(ctx)
		return len(s) == 1
	}, "series to be stored")

	// Sweeping with a zero interval treats everything as overdue.
	s := &Scheduler{app: a, interval: 0, batch: 5, tick: make(chan struct{}, 1)}
	n, err := s.sweep(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("sweep enqueued %d, want 1", n)
	}
}

// TestSchedulerDisabled covers CheckInterval=0: Run must not sweep, and must
// return when the context is cancelled rather than spinning.
func TestSchedulerDisabled(t *testing.T) {
	a := &App{Cfg: &config.Config{CheckInterval: 0, CheckBatch: 1}}
	s := NewScheduler(a)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after the context was cancelled")
	}
}

// TestModuleSettingsRoundTrip covers storing and reading back a module's
// options and login.
func TestModuleSettingsRoundTrip(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{SecretKey: "test-key"})

	settings, err := a.ModuleSettings(ctx, "TestMadara")
	if err != nil {
		t.Fatal(err)
	}
	if !settings.SecretsEnabled {
		t.Fatal("secrets should be enabled with a key configured")
	}

	if err := a.SaveModuleSettings(ctx, "TestMadara",
		map[string]string{"showgroup": "1"}, "reader", "hunter2", true); err != nil {
		t.Fatal(err)
	}

	opts, err := st.ModuleOptions(ctx, "TestMadara")
	if err != nil {
		t.Fatal(err)
	}
	if opts["showgroup"] != "1" {
		t.Errorf("option = %q, want 1", opts["showgroup"])
	}

	creds, err := st.Credentials(ctx, a.Sealer, "TestMadara")
	if err != nil {
		t.Fatal(err)
	}
	if creds == nil || creds.Username != "reader" || creds.Password != "hunter2" {
		t.Fatalf("credentials round trip gave %+v", creds)
	}

	// Clearing both fields removes the login.
	if err := a.SaveModuleSettings(ctx, "TestMadara", nil, "", "", true); err != nil {
		t.Fatal(err)
	}
	if st.HasCredentials(ctx, "TestMadara") {
		t.Error("credentials should have been removed")
	}
}

// TestCredentialsRefusedWithoutKey covers the case where no secret key is set:
// storing a password is refused rather than done in the clear.
func TestCredentialsRefusedWithoutKey(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, _, ctx := newTestApp(t, srv.URL, &config.Config{})
	if a.Sealer.Enabled() {
		t.Fatal("no key configured, sealer should be disabled")
	}
	err := a.SaveModuleSettings(ctx, "TestMadara", nil, "reader", "hunter2", true)
	if !errors.Is(err, store.ErrNoSecretKey) {
		t.Fatalf("got %v, want ErrNoSecretKey", err)
	}
}

// TestNotifyOnNewChapters covers the notification a subscription check sends.
func TestNotifyOnNewChapters(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	received := make(chan Notification, 4)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var n Notification
		_ = json.NewDecoder(r.Body).Decode(&n)
		received <- n
	}))
	t.Cleanup(hook.Close)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{NotifyURL: hook.URL})

	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, _ := st.ListSeries(ctx)
		return len(s) == 1
	}, "series to be stored")

	// The first import is not news, so nothing should have been sent yet.
	select {
	case n := <-received:
		t.Fatalf("notified on the initial import: %+v", n)
	case <-time.After(300 * time.Millisecond):
	}

	all, _ := st.ListSeries(ctx)
	chapters.Store(2)
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, all[0].ID)
		return len(chs) == 2
	}, "the new chapter to be recorded")

	select {
	case n := <-received:
		if n.Event != "new_chapters" || n.Count != 1 {
			t.Errorf("notification = %+v", n)
		}
		if n.Series != "Growing Series" || len(n.Chapters) != 1 {
			t.Errorf("notification = %+v", n)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no notification was sent for a new chapter")
	}
}

// TestCoverThumbnail covers the resize: sites publish covers at full page
// size, and a library grid would otherwise pull tens of megabytes.
func TestCoverThumbnail(t *testing.T) {
	big := image.NewRGBA(image.Rect(0, 0, 1200, 1800))
	for i := range big.Pix {
		big.Pix[i] = uint8(i % 251)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, big, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}

	out := thumbnail(buf.Bytes())
	if len(out) >= buf.Len() {
		t.Errorf("thumbnail is %d bytes, original %d — it should be smaller", len(out), buf.Len())
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(out))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != coverWidth {
		t.Errorf("width = %d, want %d", cfg.Width, coverWidth)
	}
	// Aspect ratio must survive, or every cover in the grid is distorted.
	if got, want := float64(cfg.Height)/float64(cfg.Width), 1800.0/1200.0; got < want-0.02 || got > want+0.02 {
		t.Errorf("aspect ratio = %.3f, want %.3f", got, want)
	}

	// An image already small enough is passed through untouched.
	small := image.NewRGBA(image.Rect(0, 0, 200, 300))
	var sbuf bytes.Buffer
	_ = jpeg.Encode(&sbuf, small, nil)
	if got := thumbnail(sbuf.Bytes()); len(got) != sbuf.Len() {
		t.Errorf("a small cover should pass through unchanged")
	}

	// Undecodable input must not fail the request.
	if got := thumbnail([]byte("not an image")); string(got) != "not an image" {
		t.Errorf("undecodable input should be returned as-is")
	}
}

// TestModuleKeyDiffersFromDeclaredName covers the identifier a series is
// stored against.
//
// A module is found by its file name, but 145 of 597 declare a different Name
// in Init(). Storing the label and looking modules up by it broke every
// refresh, download and settings lookup for those sites the moment a series
// was tracked — and only after tracking, so nothing before this caught it.
func TestModuleKeyDiffersFromDeclaredName(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(2)
	srv := growingSite(t, &chapters)

	// A checkout whose file name and declared name disagree, as a quarter of
	// the real catalogue does.
	upstream := upstreamLua(t)
	checkout := t.TempDir()
	luaDir := filepath.Join(checkout, "lua")
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
	m.ID              = '99999999999999999999999999999999'
	m.Name            = 'Spaced Name'
	m.RootURL         = '%s'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
end

local Template = require 'templates.Madara'
function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`, srv.URL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "SpacedName.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, LibraryDir: filepath.Join(dir, "library"),
		Workers: 1, HostConcurrency: 4, HostRPS: 1000, CheckBatch: 10,
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
	done := make(chan struct{})
	go func() { a.Pool.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	// Tracked by file name, as the site listing does.
	if err := a.EnqueueRefresh(ctx, "SpacedName", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		s, _ := st.ListSeries(ctx)
		return len(s) == 1
	}, "series to be stored")

	all, _ := st.ListSeries(ctx)
	series := all[0]
	if series.ModuleName != "Spaced Name" {
		t.Errorf("display name = %q, want the declared one", series.ModuleName)
	}
	// A site is keyed by the name it declares, not by the file it happens to
	// live in: one file can declare several sites, so the file cannot be the
	// identity.
	if series.ModuleKey != "Spaced Name" {
		t.Errorf("key = %q, want the declared site name", series.ModuleKey)
	}

	// The second refresh is what used to fail: it goes through whatever the
	// row stored, not through what the browse page passed.
	if err := a.EnqueueRefresh(ctx, series.Key(), series.URL); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		stats, _ := a.Queue.Stats(ctx)
		return stats["done"] == 2
	}, "the second refresh to succeed")
	if stats, _ := a.Queue.Stats(ctx); stats["failed"] != 0 {
		t.Errorf("a refresh failed: %+v", stats)
	}

	// Both forms resolve: the declared name, and the file name that rows
	// written before this still hold.
	if got := a.ResolveModule(ctx, "Spaced Name"); got != "Spaced Name" {
		t.Errorf("ResolveModule(name) = %q", got)
	}
	if got := a.ResolveModule(ctx, "SpacedName"); got != "Spaced Name" {
		t.Errorf("ResolveModule(file) = %q, want it to reach the site", got)
	}
}

// TestFollowIsImmediate covers the fix for the interface's worst falsehood.
//
// Following used to only enqueue a job, so the series existed nowhere until
// that job ran — and the click navigated to the library to show a result that
// was not there yet, which taught the reader to press refresh.
func TestFollowIsImmediate(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(2)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	if id == 0 {
		t.Fatal("Follow returned no id")
	}

	// Present before any job has had a chance to run.
	series, err := st.GetSeries(ctx, id)
	if err != nil {
		t.Fatalf("the series should exist the moment it is followed: %v", err)
	}
	if series.Title != "Growing Series" {
		t.Errorf("title = %q; the listing's title is used until the check fills it in", series.Title)
	}
	if series.ModuleKey != "TestMadara" {
		t.Errorf("key = %q", series.ModuleKey)
	}
	if !series.Subscribed {
		t.Error("following should subscribe")
	}
	// Not yet checked — saying otherwise would misreport the row and mislead
	// the scheduler about what is due.
	if series.CheckedAt.Valid {
		t.Error("a freshly followed series has not been checked")
	}

	// The queued check then fills in the details, without duplicating the row.
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 2
	}, "the queued check to fill in chapters")

	all, _ := st.ListSeries(ctx)
	if len(all) != 1 {
		t.Fatalf("got %d series, want 1 — the check must update the row, not add one", len(all))
	}
	if !all[0].CheckedAt.Valid {
		t.Error("the completed check should stamp checked_at")
	}

	// Following the same series again is idempotent.
	again, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Errorf("second follow returned id %d, want %d", again, id)
	}
	all, _ = st.ListSeries(ctx)
	if len(all) != 1 {
		t.Errorf("following twice created %d rows", len(all))
	}
}

// TestRecheckSite covers the loop the settings page closes: a change only
// applies from the next check, so the page offers to run them.
func TestRecheckSite(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, _, ctx := newTestApp(t, srv.URL, &config.Config{})
	if _, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series"); err != nil {
		t.Fatal(err)
	}

	n, err := a.RecheckSite(ctx, "TestMadara")
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("queued %d re-checks, want 1", n)
	}

	// A site with nothing followed from it is not an error.
	if n, err = a.RecheckSite(ctx, "NothingHere"); err != nil || n != 0 {
		t.Errorf("got (%d, %v), want (0, nil)", n, err)
	}
}

// TestTestLoginAnswers covers what the button says in each case it can reach
// without a site that actually authenticates.
func TestTestLoginAnswers(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	// No key configured at all.
	a, _, ctx := newTestApp(t, srv.URL, &config.Config{})
	if ok, detail := a.TestLogin(ctx, "TestMadara", "", ""); ok || !strings.Contains(detail, "secret key") {
		t.Errorf("got (%v, %q), want a note about the missing key", ok, detail)
	}

	// Key configured, nothing saved and nothing typed.
	b, _, bctx := newTestApp(t, srv.URL, &config.Config{SecretKey: "k"})
	if ok, detail := b.TestLogin(bctx, "TestMadara", "", ""); ok || !strings.Contains(detail, "Type a username") {
		t.Errorf("got (%v, %q), want an invitation to type something", ok, detail)
	}

	// Typed but not saved: the test must reach the module rather than stop at
	// the empty database, because being told to save first in order to find
	// out whether a password works has the order backwards.
	ok, detail := b.TestLogin(bctx, "TestMadara", "reader", "pw")
	if ok || !strings.Contains(detail, "does not take a login") {
		t.Errorf("got (%v, %q), want the module's own verdict", ok, detail)
	}

	// Saved, and still reaching the module.
	if err := b.SaveModuleSettings(bctx, "TestMadara", nil, "reader", "pw", true); err != nil {
		t.Fatal(err)
	}
	if ok, detail := b.TestLogin(bctx, "TestMadara", "", ""); ok || !strings.Contains(detail, "does not take a login") {
		t.Errorf("got (%v, %q), want the module's own verdict", ok, detail)
	}
}

// TestBacklogIsNotNews covers the distinction the library is grouped by.
//
// Every chapter is "not downloaded" on the first check, and treating that as
// news would fill the new-chapters section with back catalogues. Only what
// turns up on a later check is news.
func TestBacklogIsNotNews(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(3)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})
	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 3
	}, "the first check")

	progress, err := st.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := progress[id]; p.New != 0 {
		t.Errorf("first check reported %d new; the whole list was already published", p.New)
	}
	if p := progress[id]; p.Waiting != 3 {
		t.Errorf("waiting = %d, want the 3 chapters of backlog", p.Waiting)
	}

	// A chapter comes out.
	chapters.Store(4)
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 4
	}, "the second check")

	progress, err = st.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	p := progress[id]
	if p.New != 1 {
		t.Errorf("new = %d, want the one chapter that came out", p.New)
	}
	if !p.NewestArrival.Valid {
		t.Error("a new chapter should record when it arrived")
	}
	if p.Waiting != 4 {
		t.Errorf("waiting = %d, want all four still undownloaded", p.Waiting)
	}
}

// TestCheckKeepsWhatItCannotFind covers a check that scrapes less than the
// listing already knew.
//
// A module that returns an empty title used to overwrite the good one, and
// the library then held a row with nothing to click. A scraped title also
// carries the indentation of the page it was cut from, which sorted it before
// every letter in a list that is ordered by title.
func TestCheckKeepsWhatItCannotFind(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "\n\t  Growing Series  \n")
	if err != nil {
		t.Fatal(err)
	}
	series, err := st.GetSeries(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if series.Title != "Growing Series" {
		t.Errorf("stored title = %q, want it trimmed", series.Title)
	}

	// The test site reports a title, so the check should adopt it; what it
	// must never do is replace a title with nothing.
	if _, err := st.UpsertSeries(ctx, store.Series{
		ModuleID: series.ModuleID, ModuleKey: series.ModuleKey,
		ModuleName: series.ModuleName, URL: series.URL,
		Title: "", CoverURL: "",
	}); err != nil {
		t.Fatal(err)
	}
	series, err = st.GetSeries(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if series.Title != "Growing Series" {
		t.Errorf("title = %q; an empty scrape must not erase it", series.Title)
	}
}

// twoSiteCheckout builds a module checkout holding one file that declares two
// websites and one that declares a site under two addresses — the two shapes
// the upstream catalogue uses and atsume used to collapse.
func twoSiteCheckout(t *testing.T, rootURL string) string {
	t.Helper()
	upstream := upstreamLua(t)
	checkout := t.TempDir()
	luaDir := filepath.Join(checkout, "lua")
	if err := os.MkdirAll(filepath.Join(luaDir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []string{"templates", "utils"} {
		if err := os.Symlink(filepath.Join(upstream, shared), filepath.Join(luaDir, shared)); err != nil {
			t.Fatal(err)
		}
	}

	pair := fmt.Sprintf(`
function Init()
	function AddWebsiteModule(id, name, url)
		local m = NewWebsiteModule()
		m.ID              = id
		m.Name            = name
		m.RootURL         = url
		m.Category        = 'English'
		m.OnGetInfo       = 'GetInfo'
		m.OnGetPageNumber = 'GetPageNumber'
		m.AddOptionComboBox('imagesize', 'Image size:', 'Auto\nOriginal', 0)
		return m
	end
	AddWebsiteModule('aaaa', 'Front Door', '%s')
	local m = AddWebsiteModule('bbbb', 'Side Door', '%s')
	m.AccountSupport = true
	m.OnLogin        = 'SideLogin'
end

function SideLogin() return true end

local Template = require 'templates.Madara'
function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`, rootURL, rootURL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "Doors.lua"), []byte(pair), 0o644); err != nil {
		t.Fatal(err)
	}

	mirrored := fmt.Sprintf(`
function Init()
	local function AddWebsiteModule(id, url)
		local m = NewWebsiteModule()
		m.ID        = id
		m.Name      = 'Mirrored'
		m.RootURL   = url
		m.Category  = 'English'
		m.OnGetInfo = 'GetInfo'
	end
	AddWebsiteModule('cccc', '%s')
	AddWebsiteModule('dddd', 'https://second.example')
end

local Template = require 'templates.Madara'
function GetInfo() Template.GetInfo() return no_error end
`, rootURL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "Mirrored.lua"), []byte(mirrored), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// newCheckoutApp starts an app against a prepared module checkout.
func newCheckoutApp(t *testing.T, checkout string) (*App, *store.Store, context.Context) {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{
		DataDir: dir, LibraryDir: filepath.Join(dir, "library"),
		Workers: 1, HostConcurrency: 4, HostRPS: 1000, CheckBatch: 10,
		SecretKey: "test-key",
	}
	st, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	reg := scraper.NewRegistry(filepath.Join(dir, "modules"), "")
	if err := reg.Use(checkout, "test"); err != nil {
		t.Fatal(err)
	}
	a := New(cfg, st, reg)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	done := make(chan struct{})
	go func() { a.Pool.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return a, st, ctx
}

// TestCatalogueListsSitesNotFiles covers the identity the whole interface is
// keyed by.
//
// A file was taken to be a site, so a file declaring two exposed only the
// last one — sixty-one sites were unreachable, and the twenty-eight that were
// reachable answered to the wrong name. Mirrors are the opposite case: one
// site under several addresses, which must not become several entries.
func TestCatalogueListsSitesNotFiles(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, _, ctx := newCheckoutApp(t, twoSiteCheckout(t, srv.URL))

	cat := a.ModuleCatalogue(ctx)
	byName := map[string]ModuleEntry{}
	for _, e := range cat.Entries {
		byName[e.Site] = e
	}
	// Two files, three sites.
	if len(cat.Entries) != 3 {
		t.Fatalf("got %d entries, want three sites from two files: %+v", len(cat.Entries), cat.Entries)
	}
	for _, want := range []string{"Front Door", "Side Door", "Mirrored"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("%q is missing from the catalogue", want)
		}
	}
	// Only the second site in the pair takes a login, and that must not leak
	// onto the first: it is why a login went to the wrong domain.
	if byName["Front Door"].NeedsLogin {
		t.Error("Front Door does not take a login")
	}
	if !byName["Side Door"].NeedsLogin {
		t.Error("Side Door does take a login")
	}
	// Mirrors are one entry with several addresses, not several entries.
	if got := byName["Mirrored"].Mirrors; len(got) != 2 {
		t.Errorf("Mirrored has %d addresses, want 2", len(got))
	}
	if !byName["Mirrored"].HasMirrors() || byName["Front Door"].HasMirrors() {
		t.Error("only the mirrored site offers a choice of address")
	}
}

// TestSitesInOneFileAreConfiguredApart covers the settings each site keeps.
func TestSitesInOneFileAreConfiguredApart(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, _, ctx := newCheckoutApp(t, twoSiteCheckout(t, srv.URL))

	front, err := a.ModuleSettings(ctx, "Front Door")
	if err != nil {
		t.Fatal(err)
	}
	// Declarations used to share one option slice, so each site showed both
	// sites' options — the same dropdown, listed twice.
	if len(front.Options) != 1 {
		t.Errorf("Front Door shows %d options, want its own one", len(front.Options))
	}
	if front.SupportsLogin {
		t.Error("Front Door takes no login")
	}

	side, err := a.ModuleSettings(ctx, "Side Door")
	if err != nil {
		t.Fatal(err)
	}
	if !side.SupportsLogin {
		t.Error("Side Door takes a login")
	}

	// A setting saved on one site does not appear on the other.
	if err := a.SaveModuleSettings(ctx, "Front Door", map[string]string{"imagesize": "1"}, "", "", false); err != nil {
		t.Fatal(err)
	}
	front, _ = a.ModuleSettings(ctx, "Front Door")
	side, _ = a.ModuleSettings(ctx, "Side Door")
	if front.Values["imagesize"] != "1" {
		t.Errorf("Front Door kept %q", front.Values["imagesize"])
	}
	if side.Values["imagesize"] == "1" {
		t.Error("the setting leaked onto the other site in the same file")
	}
}

// TestMirrorIsChosenAndUsed covers why mirrors exist: the address in use has
// to be changeable when one stops answering.
func TestMirrorIsChosenAndUsed(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, _, ctx := newCheckoutApp(t, twoSiteCheckout(t, srv.URL))

	s, err := a.ModuleSettings(ctx, "Mirrored")
	if err != nil {
		t.Fatal(err)
	}
	if s.Mirror != srv.URL {
		t.Errorf("address in use = %q, want the first declared", s.Mirror)
	}

	if err := a.SaveModuleSettings(ctx, "Mirrored",
		map[string]string{MirrorOption: "https://second.example"}, "", "", false); err != nil {
		t.Fatal(err)
	}
	if s, _ = a.ModuleSettings(ctx, "Mirrored"); s.Mirror != "https://second.example" {
		t.Errorf("address in use = %q, want the chosen one", s.Mirror)
	}

	// An address upstream no longer declares is ignored rather than honoured.
	if err := a.SaveModuleSettings(ctx, "Mirrored",
		map[string]string{MirrorOption: "https://gone.example"}, "", "", false); err != nil {
		t.Fatal(err)
	}
	if s, _ = a.ModuleSettings(ctx, "Mirrored"); s.Mirror != srv.URL {
		t.Errorf("address in use = %q, want a fallback to the first", s.Mirror)
	}
}

// TestRepairModuleKeys covers series followed before a file was understood to
// hold more than one site.
func TestRepairModuleKeys(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, st, ctx := newCheckoutApp(t, twoSiteCheckout(t, srv.URL))

	// A row as the old code wrote it: keyed by the file, named for whichever
	// site happened to be declared last.
	id, err := st.UpsertSeries(ctx, store.Series{
		ModuleID: "bbbb", ModuleKey: "Doors", ModuleName: "Side Door",
		URL: srv.URL + "/manga/grow/", Title: "Old Row", Subscribed: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if err := a.RepairModuleKeys(ctx); err != nil {
		t.Fatal(err)
	}
	v, err := st.GetSeries(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The site it actually scraped, not the file, and not the file's first
	// site — the name it recorded says which one it was.
	if v.ModuleKey != "Side Door" {
		t.Errorf("key = %q, want the site it came from", v.ModuleKey)
	}

	// And the settings page for that site now finds it.
	s, err := a.ModuleSettings(ctx, "Side Door")
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Series) != 1 {
		t.Errorf("Side Door lists %d series, want the repaired one", len(s.Series))
	}
	if s, _ = a.ModuleSettings(ctx, "Front Door"); len(s.Series) != 0 {
		t.Errorf("Front Door lists %d series, want none", len(s.Series))
	}
}

// TestRemoveKeepsTheFiles is the test behind the promise the confirmation
// makes.
//
// Removing a series forgets it here and leaves everything already written on
// disk, because those files belong to whatever reads the destination folder.
// A program that reaches into a media library to delete is one you forgive
// once, so this is pinned rather than trusted.
func TestRemoveKeepsTheFiles(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 1
	}, "the first check")
	if _, err := a.EnqueueAllPending(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 1 && chs[0].State == store.ChapterDone
	}, "the chapter to be written")

	chs, _ := st.ListChapters(ctx, id)
	written := chs[0].FilePath
	if written == "" {
		t.Fatal("no file was recorded for the downloaded chapter")
	}
	if _, err := os.Stat(written); err != nil {
		t.Fatalf("the chapter should be on disk before the test means anything: %v", err)
	}

	// A refresh is in the queue when the removal happens. Left there it would
	// run afterwards and recreate the series from the site, which is the one
	// way a removal could appear not to have worked.
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteSeries(ctx, id); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(written); err != nil {
		t.Errorf("removing a series deleted %s: %v", written, err)
	}
	if _, err := st.GetSeries(ctx, id); err == nil {
		t.Error("the series is still recorded")
	}
	if chs, _ := st.ListChapters(ctx, id); len(chs) != 0 {
		t.Errorf("%d chapters outlived the series", len(chs))
	}

	// Give the pool a chance to pick up anything still queued, then confirm
	// nothing brought the series back.
	waitFor(t, ctx, func() bool {
		stats, _ := a.Queue.Stats(ctx)
		return stats["pending"] == 0 && stats["running"] == 0
	}, "the queue to drain")
	all, _ := st.ListSeries(ctx)
	if len(all) != 0 {
		t.Errorf("a queued job resurrected the series: %+v", all)
	}
}

// TestMissingFileIsNoticedAndRecoverable covers what happens when a file
// leaves the library without atsume doing it.
//
// The directory belongs to whatever reads it — a chapter deleted through
// Komga, a volume that was not mounted. The row went on saying "done"
// forever, the library counted a file that was not there, and the interface
// offered no way to fetch it again.
func TestMissingFileIsNoticedAndRecoverable(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 1
	}, "the first check")
	if _, err := a.EnqueueAllPending(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 1 && chs[0].State == store.ChapterDone
	}, "the chapter to be written")

	chs, _ := st.ListChapters(ctx, id)
	written := chs[0].FilePath

	// Nothing is missing while the file is there.
	if n := len(a.MissingFiles(chs)); n != 0 {
		t.Fatalf("%d files reported missing before anything was deleted", n)
	}

	// Somebody deletes it through the library server.
	if err := os.Remove(written); err != nil {
		t.Fatal(err)
	}
	if missing := a.MissingFiles(chs); !missing[chs[0].ID] {
		t.Error("a deleted file should be noticed")
	}

	// "Download everything waiting" has to include it. Skipping exactly the
	// chapters a reader just deleted is the one thing that action must not
	// do.
	n, err := a.EnqueueAllPending(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("queued %d, want the missing chapter", n)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return chs[0].State == store.ChapterDone
	}, "the chapter to be written again")
	if _, err := os.Stat(written); err != nil {
		t.Errorf("the file was not restored: %v", err)
	}
}

// TestCheckReconcilesDeletedFiles covers the other half: the library must
// stop claiming a file it no longer has, without a page load going near the
// filesystem.
func TestCheckReconcilesDeletedFiles(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)
	a, st, ctx := newTestApp(t, srv.URL, &config.Config{})

	id, err := a.Follow(ctx, "TestMadara", srv.URL+"/manga/grow/", "Growing Series")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return len(chs) == 1
	}, "the first check")
	if _, err := a.EnqueueAllPending(ctx, id); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return chs[0].State == store.ChapterDone
	}, "the chapter to be written")

	chs, _ := st.ListChapters(ctx, id)
	if err := os.Remove(chs[0].FilePath); err != nil {
		t.Fatal(err)
	}

	// The next scheduled check notices.
	if err := a.EnqueueRefresh(ctx, "TestMadara", srv.URL+"/manga/grow/"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		chs, _ := st.ListChapters(ctx, id)
		return chs[0].State == store.ChapterPending
	}, "the check to notice the file had gone")

	// The library counts it as outstanding rather than as something it has.
	progress, err := st.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p := progress[id]; p.Done != 0 || p.Waiting != 1 {
		t.Errorf("progress = %+v, want nothing downloaded and one waiting", p)
	}

	// And it is not fetched again behind the reader's back: a file that
	// disappeared may have been deleted on purpose. Only a newly published
	// chapter downloads by itself.
	chs, _ = st.ListChapters(ctx, id)
	if chs[0].FilePath != "" {
		t.Errorf("stale path kept: %q", chs[0].FilePath)
	}
	if _, err := os.Stat(filepath.Join(a.Cfg.LibraryDir, "Growing Series")); err == nil {
		entries, _ := os.ReadDir(filepath.Join(a.Cfg.LibraryDir, "Growing Series"))
		if len(entries) != 0 {
			t.Errorf("the check re-downloaded on its own: %d files", len(entries))
		}
	}
}

// sectionedSite serves a directory split into three parts, the first of
// which is empty — the shape that made ComicExtra list nothing at all, since
// its first section is "others".
func sectionedSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	// Section 0 has nothing; section 1 has two pages; section 2 has one.
	titles := map[string][]string{
		"1/0": {"Beta One", "Beta Two"},
		"1/1": {"Beta Three"},
		"2/0": {"Gamma One"},
	}
	mux.HandleFunc("/dir/", func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/dir/")
		var b strings.Builder
		b.WriteString(`<html><body><ul class="manga-list">`)
		for _, name := range titles[key] {
			fmt.Fprintf(&b, `<li><a href="/manga/%s/">%s</a></li>`,
				strings.ToLower(strings.ReplaceAll(name, " ", "-")), name)
		}
		b.WriteString(`</ul></body></html>`)
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, b.String())
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// sectionedCheckout writes a module that splits its directory into sections,
// the way thirty-one upstream modules do.
func sectionedCheckout(t *testing.T, rootURL string) string {
	t.Helper()
	upstream := upstreamLua(t)
	checkout := t.TempDir()
	luaDir := filepath.Join(checkout, "lua")
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
	m.ID               = 'sectioned'
	m.Name             = 'Sectioned'
	m.RootURL          = '%s'
	m.Category         = 'English'
	m.TotalDirectory   = 3
	m.OnGetNameAndLink = 'GetNameAndLink'
end

function GetNameAndLink()
	local u = MODULE.RootURL .. '/dir/' .. MODULE.CurrentDirectoryIndex .. '/' .. URL
	if not HTTP.GET(u) then return net_problem end
	CreateTXQuery(HTTP.Document).XPathHREFAll('//ul[@class="manga-list"]/li/a', LINKS, NAMES)
	return no_error
end
`, rootURL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "Sectioned.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return checkout
}

// TestBrowseWalksEverySection covers a directory that is not one list.
//
// Thirty-one modules split theirs into sections — an alphabet with a page
// per letter, or ongoing and finished — and atsume only ever read the first.
// ComicExtra's first section is "others", so the site listed nothing at all.
func TestBrowseWalksEverySection(t *testing.T) {
	srv := sectionedSite(t)
	a, _, ctx := newCheckoutApp(t, sectionedCheckout(t, srv.URL))

	// The first section is empty, so the first request must not come back
	// empty: it rolls on until it finds something.
	res, err := a.Browse(ctx, "Sectioned", BrowsePos{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Sections != 3 {
		t.Errorf("sections = %d, want 3", res.Sections)
	}
	if len(res.Entries) != 2 || res.Entries[0].Name != "Beta One" {
		t.Fatalf("first screenful = %+v", res.Entries)
	}
	if res.At.Dir != 1 || res.At.Page != 0 {
		t.Errorf("landed at %+v, want the start of section 2", res.At)
	}

	// Continuing stays in the section while it has pages.
	res, err = a.Browse(ctx, "Sectioned", res.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "Beta Three" {
		t.Fatalf("second screenful = %+v", res.Entries)
	}

	// And then crosses into the next one rather than stopping.
	res, err = a.Browse(ctx, "Sectioned", res.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 1 || res.Entries[0].Name != "Gamma One" {
		t.Fatalf("third screenful = %+v", res.Entries)
	}
	if res.At.Dir != 2 {
		t.Errorf("landed at %+v, want the last section", res.At)
	}

	// The end is the end: nothing left, and nothing offered.
	res, err = a.Browse(ctx, "Sectioned", res.Next)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Entries) != 0 || res.More {
		t.Errorf("past the end: %d entries, more=%v", len(res.Entries), res.More)
	}
}

// TestSiteCatalogueIsKept covers the complaint this exists for.
//
// A site's title list was fetched and thrown away on every visit. For a
// module that pages the site internally — MangaToon takes three minutes to
// hand back 2,677 titles — that meant three minutes of someone else's
// bandwidth per look, and a reader who could not leave the page.
func TestSiteCatalogueIsKept(t *testing.T) {
	var reads atomic.Int32
	srv := countingSite(t, &reads)
	a, st, ctx := newCheckoutApp(t, sectionedCheckout(t, srv.URL))

	// Nothing is known until it is read, and that is distinguishable from a
	// site read and found empty.
	info, err := st.SiteCatalogueInfo(ctx, "Sectioned")
	if err != nil {
		t.Fatal(err)
	}
	if info.Exists {
		t.Fatal("a site nobody has read should not claim a catalogue")
	}

	if err := a.EnqueueIndex(ctx, "Sectioned"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		info, _ := st.SiteCatalogueInfo(ctx, "Sectioned")
		return info.Complete
	}, "the catalogue to be read")

	info, _ = st.SiteCatalogueInfo(ctx, "Sectioned")
	if info.Titles != 4 {
		t.Errorf("stored %d titles, want every one across the sections", info.Titles)
	}
	if info.BuiltAt.IsZero() {
		t.Error("a stored list has to say when it was read")
	}
	after := reads.Load()
	if after == 0 {
		t.Fatal("the site was never asked")
	}

	// Looking again costs the site nothing. That is the whole point.
	titles, found, err := st.SearchSiteTitles(ctx, "Sectioned", "", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if found != 4 || len(titles) != 4 {
		t.Fatalf("found=%d returned=%d", found, len(titles))
	}
	if reads.Load() != after {
		t.Errorf("reading the stored list went back to the site")
	}

	// Order is the site's own, which usually means something.
	if titles[0].Name != "Beta One" || titles[3].Name != "Gamma One" {
		t.Errorf("order = %q … %q", titles[0].Name, titles[3].Name)
	}

	// Search runs over the whole catalogue, not over what a browser happens
	// to have loaded.
	titles, found, err = st.SearchSiteTitles(ctx, "Sectioned", "gamma", 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	if found != 1 || titles[0].Name != "Gamma One" {
		t.Errorf("search found %d: %+v", found, titles)
	}

	// A term with wildcards in it is a term, not a pattern.
	if _, found, _ = st.SearchSiteTitles(ctx, "Sectioned", "%", 0, 50); found != 0 {
		t.Errorf("a literal %% matched %d titles", found)
	}

	// Reading again replaces rather than accumulates, so a title the site
	// has dropped does not live forever.
	if err := a.EnqueueIndex(ctx, "Sectioned"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		stats, _ := a.Queue.Stats(ctx)
		return stats["pending"] == 0 && stats["running"] == 0
	}, "the second read")
	info, _ = st.SiteCatalogueInfo(ctx, "Sectioned")
	if info.Titles != 4 {
		t.Errorf("after a second read the catalogue holds %d titles", info.Titles)
	}
}

// countingSite is the sectioned test site with a request counter, so a test
// can tell whether looking at a catalogue went back to the network.
func countingSite(t *testing.T, reads *atomic.Int32) *httptest.Server {
	t.Helper()
	inner := sectionedSite(t)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		res, err := http.Get(inner.URL + r.URL.RequestURI())
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.Header().Set("Content-Type", res.Header.Get("Content-Type"))
		io.Copy(w, res.Body)
	}))
	t.Cleanup(proxy.Close)
	return proxy
}

// TestIndexStopsWhenNothingIsNew covers a module that ignores the page it is
// asked for.
//
// MangaToon's GetNameAndLink walks the site's own "Next Page" links
// internally and hands back the whole catalogue whatever page it is given,
// so every position returns the same 2,677 titles. A read that stopped only
// when a position came back empty would never stop — and each position
// costs three minutes of the site's bandwidth.
func TestIndexStopsWhenNothingIsNew(t *testing.T) {
	var calls atomic.Int32
	mux := http.NewServeMux()
	mux.HandleFunc("/all", func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<html><body><ul class="manga-list">
			<li><a href="/manga/one/">One</a></li>
			<li><a href="/manga/two/">Two</a></li>
		</ul></body></html>`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	upstream := upstreamLua(t)
	checkout := t.TempDir()
	luaDir := filepath.Join(checkout, "lua")
	if err := os.MkdirAll(filepath.Join(luaDir, "modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, shared := range []string{"templates", "utils"} {
		if err := os.Symlink(filepath.Join(upstream, shared), filepath.Join(luaDir, shared)); err != nil {
			t.Fatal(err)
		}
	}
	// The page it is given is ignored, exactly as upstream's does.
	src := fmt.Sprintf(`
function Init()
	local m = NewWebsiteModule()
	m.ID               = 'whole'
	m.Name             = 'Whole'
	m.RootURL          = '%s'
	m.OnGetNameAndLink = 'GetNameAndLink'
end

function GetNameAndLink()
	if not HTTP.GET(MODULE.RootURL .. '/all') then return net_problem end
	CreateTXQuery(HTTP.Document).XPathHREFAll('//ul[@class="manga-list"]/li/a', LINKS, NAMES)
	return no_error
end
`, srv.URL)
	if err := os.WriteFile(filepath.Join(luaDir, "modules", "Whole.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	a, st, ctx := newCheckoutApp(t, checkout)
	if err := a.EnqueueIndex(ctx, "Whole"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, ctx, func() bool {
		info, _ := st.SiteCatalogueInfo(ctx, "Whole")
		return info.Complete
	}, "the read to finish rather than loop forever")

	info, _ := st.SiteCatalogueInfo(ctx, "Whole")
	if info.Titles != 2 {
		t.Errorf("stored %d titles, want 2 without duplicates", info.Titles)
	}
	// Two requests: the one that returned the list, and the one that proved
	// there was nothing after it.
	if n := calls.Load(); n > 3 {
		t.Errorf("asked the site %d times for a catalogue it hands over whole", n)
	}
}
