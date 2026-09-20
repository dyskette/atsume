package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
