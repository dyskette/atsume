package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
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
	if series.ModuleKey != "SpacedName" {
		t.Errorf("key = %q, want the file name", series.ModuleKey)
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

	// And a label-only lookup still resolves, for rows written before the two
	// were told apart.
	if got := a.ResolveModule(ctx, "Spaced Name"); got != "SpacedName" {
		t.Errorf("ResolveModule(label) = %q, want the file name", got)
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

// TestTestLoginWithoutCredentials covers the answers the button gives when
// there is nothing to test, which are the common cases.
func TestTestLoginWithoutCredentials(t *testing.T) {
	var chapters atomic.Int32
	chapters.Store(1)
	srv := growingSite(t, &chapters)

	// No key configured at all.
	a, _, ctx := newTestApp(t, srv.URL, &config.Config{})
	if ok, detail := a.TestLogin(ctx, "TestMadara"); ok || !strings.Contains(detail, "secret key") {
		t.Errorf("got (%v, %q), want a note about the missing key", ok, detail)
	}

	// Key configured, nothing saved.
	b, _, bctx := newTestApp(t, srv.URL, &config.Config{SecretKey: "k"})
	if ok, detail := b.TestLogin(bctx, "TestMadara"); ok || !strings.Contains(detail, "No username") {
		t.Errorf("got (%v, %q), want a note that nothing is saved", ok, detail)
	}

	// Saved, but the module takes no login.
	if err := b.SaveModuleSettings(bctx, "TestMadara", nil, "reader", "pw", true); err != nil {
		t.Fatal(err)
	}
	if ok, detail := b.TestLogin(bctx, "TestMadara"); ok || !strings.Contains(detail, "does not take a login") {
		t.Errorf("got (%v, %q), want a note that the site takes no login", ok, detail)
	}
}
