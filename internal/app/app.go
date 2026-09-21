// Package app wires the scraper, queue and store together and implements the
// job handlers.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/download"
	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// App holds the long-lived collaborators.
type App struct {
	Cfg      *config.Config
	Store    *store.Store
	Queue    *jobs.Queue
	Bus      *jobs.Bus
	Registry *scraper.Registry
	// Sealer encrypts stored module credentials.
	Sealer    *store.Sealer
	Limiter   *scraper.HostLimiter
	Pool      *jobs.Pool
	Scheduler *Scheduler
	Notifier  *Notifier
	Solver    *scraper.Flaresolverr
	// Transport, when set, replaces the default HTTP transport everywhere.
	Transport http.RoundTripper

	catalogue catalogueCache
	// started is when this process came up, which is what tells a scheduler
	// that has never run apart from one that started a moment ago.
	started time.Time
}

// Uptime is how long this process has been running.
func (a *App) Uptime() time.Duration { return time.Since(a.started) }

// New builds an App from its dependencies.
func New(cfg *config.Config, st *store.Store, reg *scraper.Registry) *App {
	a := &App{
		Cfg:      cfg,
		Sealer:   store.NewSealer(cfg.SecretKey),
		Store:    st,
		Queue:    jobs.NewQueue(st.DB),
		Bus:      jobs.NewBus(),
		Registry: reg,
		Limiter:  scraper.NewHostLimiter(cfg.HostRPS, cfg.HostConcurrency),
		started:  time.Now(),
	}
	a.Pool = &jobs.Pool{
		Queue:   a.Queue,
		Bus:     a.Bus,
		Workers: cfg.Workers,
		Handlers: map[string]jobs.Handler{
			jobs.KindRefreshSeries:   a.refreshSeries,
			jobs.KindDownloadChapter: a.downloadChapter,
			jobs.KindIndexSite:       a.indexSite,
		},
	}
	a.Scheduler = NewScheduler(a)
	a.Notifier = NewNotifier(cfg.NotifyURL, a.Transport)
	a.Solver = scraper.NewFlaresolverr(cfg.FlaresolverrURL)
	return a
}

// errModuleNotFound names the revision, because the usual cause is a module
// that exists upstream but not in the pinned checkout.
func errModuleNotFound(name, ref string) error {
	return fmt.Errorf("no module named %q in revision %s", name, ref)
}

// openModule loads a module into a fresh Lua state. Each call gets its own
// state because the module API is built on globals and a state is not safe for
// concurrent use.
func (a *App) openModule(ctx context.Context, name string) (*scraper.Runner, error) {
	e, ok := a.SiteInfo(ctx, name)
	if !ok {
		return nil, errModuleNotFound(name, a.Registry.Ref())
	}
	r, err := a.openFileRaw(ctx, e.File, e.Site, a.mirrorFor(ctx, e))
	if err != nil {
		return nil, err
	}
	if opts, err := a.Store.ModuleOptions(ctx, e.Site); err == nil {
		for k, v := range opts {
			r.SetOptionString(k, v)
		}
	}
	if err := a.applyCredentials(ctx, r); err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}

// applyCredentials supplies a stored login and runs the module's OnLogin
// handler.
//
// A failed login is fatal for the scrape rather than ignored: a module that
// needs an account and did not get one returns a teaser page, which would
// otherwise be recorded as the real chapter list.
func (a *App) applyCredentials(ctx context.Context, r *scraper.Runner) error {
	name := r.Module().Name
	if !r.HasHandler("OnLogin") || !a.Store.HasCredentials(ctx, name) {
		return nil
	}
	creds, err := a.Store.Credentials(ctx, a.Sealer, name)
	if err != nil {
		return fmt.Errorf("%s credentials: %w", name, err)
	}
	if creds == nil {
		return nil
	}

	r.SetAccount(creds.Username, creds.Password)
	ok, err := r.Login()
	if err != nil {
		return fmt.Errorf("%s login: %w", name, err)
	}
	if !ok {
		return fmt.Errorf("%s: login failed", name)
	}
	slog.Info("module login succeeded", "module", name, "user", creds.Username)
	return nil
}

// Browse lists one page of a site's directory.
// BrowsePos is a place in a site's directory: which section, and which page
// of it.
//
// A site is not always one list. Thirty-one modules split their directory
// into sections — an alphabet with a page per letter, or ongoing, finished
// and one-shots — and atsume only ever read the first, so ComicExtra showed
// nothing at all because its first section is "others".
type BrowsePos struct {
	Dir  int
	Page int
}

// BrowseResult is one screenful of a site's directory.
type BrowseResult struct {
	Entries []scraper.Entry
	// At is where these entries came from, which is not always where they
	// were asked for: an exhausted section rolls on to the next.
	At BrowsePos
	// Next is where to continue, and More whether there is anywhere to go.
	Next BrowsePos
	More bool
	// Sections is how many the site is split into, so the reader can be told
	// when they cross from one into another.
	Sections int
}

// maxBrowseRollovers bounds how many empty sections one request will skip.
//
// Rolling on is a request to the site each time, and a site with an
// alphabetical directory can have twenty-seven of them. Stopping after a few
// keeps one click from becoming a burst.
const maxBrowseRollovers = 4

// Browse reads one page of a site's directory, walking into the next section
// when the current one is finished.
//
// The sections are concatenated rather than offered as a choice, because a
// module declares only how many there are and never what they are called.
// "Section 3 of 27" is not a thing anyone wants to pick; they want the
// titles.
func (a *App) Browse(ctx context.Context, module string, at BrowsePos) (BrowseResult, error) {
	r, err := a.openModule(ctx, module)
	if err != nil {
		return BrowseResult{}, err
	}
	defer r.Close()

	total := r.TotalDirectories()
	out := BrowseResult{At: at, Next: at, Sections: total}
	if at.Dir < 0 {
		at.Dir = 0
	}
	for attempt := 0; attempt < maxBrowseRollovers && at.Dir < total; attempt++ {
		r.SetDirectoryIndex(at.Dir)
		entries, err := r.GetNameAndLink(at.Page)
		if err != nil {
			return out, err
		}
		if len(entries) > 0 {
			out.Entries = entries
			out.At = at
			out.Next = BrowsePos{Dir: at.Dir, Page: at.Page + 1}
			out.More = true
			return out, nil
		}
		// Nothing here. The section is finished, or was always empty.
		at = BrowsePos{Dir: at.Dir + 1}
	}
	out.At, out.Next = at, at
	out.More = at.Dir < total
	return out, nil
}

// RefreshPayload identifies a series to refresh.
type RefreshPayload struct {
	Module string `json:"module"`
	URL    string `json:"url"`
}

// EnqueueRefresh queues a metadata and chapter-list refresh.
func (a *App) EnqueueRefresh(ctx context.Context, module, url string) error {
	if _, err := a.Queue.Enqueue(ctx, jobs.KindRefreshSeries, RefreshPayload{Module: module, URL: url}); err != nil {
		return err
	}
	a.Pool.Notify()
	return nil
}

func (a *App) refreshSeries(ctx context.Context, raw json.RawMessage) error {
	var p RefreshPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	r, err := a.openModule(ctx, p.Module)
	if err != nil {
		return err
	}
	defer r.Close()

	info, err := r.GetInfo(p.URL)
	if err != nil {
		return err
	}
	mod := r.Module()

	id, err := a.Store.UpsertSeries(ctx, store.Series{
		ModuleID: mod.ID,
		// The key is what the registry indexes by; the name is only displayed.
		ModuleKey:  a.ResolveModule(ctx, p.Module),
		ModuleName: mod.Name,
		URL:        p.URL,
		Title:      info.Title,
		CoverURL:   info.CoverLink,
		Authors:    info.Authors,
		Artists:    info.Artists,
		Genres:     info.Genres,
		Status:     info.Status,
		Summary:    info.Summary,
		Subscribed: true,
	})
	if err != nil {
		return err
	}

	links, names := info.ChapterLinks.All(), info.ChapterNames.All()
	chs := make([]store.Chapter, 0, len(links))
	for i, link := range links {
		name := link
		if i < len(names) {
			name = names[i]
		}
		parsed := download.ParseChapter(info.Title, name)
		chs = append(chs, store.Chapter{
			URL: link, Name: name, Number: parsed.Number, Volume: parsed.Volume,
		})
	}
	// Whether this is the first look at the series decides whether what it
	// turns up is news or a back catalogue, and queuing a whole back
	// catalogue is rarely what following a series was meant to do.
	added, initialImport, err := a.Store.ReplaceChapters(ctx, id, chs)
	if err != nil {
		return err
	}

	message := fmt.Sprintf("%s: %d chapters", info.Title, len(chs))
	if len(added) > 0 && !initialImport {
		message = fmt.Sprintf("%s: %d new chapter(s)", info.Title, len(added))
		slog.Info("new chapters", "series", info.Title, "count", len(added))

		names := make([]string, 0, len(added))
		for _, c := range added {
			names = append(names, c.Name)
		}
		a.Notifier.Notify(ctx, newChaptersMessage(id, info.Title, mod.Name, names))

		if a.Cfg.AutoDownload {
			for _, c := range added {
				if err := a.EnqueueDownload(ctx, c.ID); err != nil {
					return err
				}
			}
		}
	}

	// The check is also the moment to notice that files have left. Doing it
	// here rather than while rendering keeps a page load off the filesystem
	// and keeps a GET from quietly rewriting rows.
	if gone, err := a.reconcileFiles(ctx, id); err != nil {
		slog.Warn("could not check files on disk", "series", info.Title, "err", err)
	} else if gone > 0 {
		message += fmt.Sprintf(" · %s missing from disk", plural(gone, "file"))
	}

	a.Bus.Publish(jobs.Event{
		Kind: "series-updated", SeriesID: id, State: "done", Message: message,
	})
	return nil
}

// reconcileFiles marks chapters whose file has left as outstanding again,
// returning how many.
//
// The library directory belongs to whatever reads it, and files leave without
// atsume doing it: deleted through Komga, lost with an unmounted volume,
// tidied away. Until this the row went on saying "done" forever, and the
// library counted a file that was not there.
//
// A chapter reset this way is not re-fetched on its own. Only a newly
// published chapter is downloaded automatically; a file that disappeared may
// have been deleted on purpose, and the reader decides whether to bring it
// back.
func (a *App) reconcileFiles(ctx context.Context, seriesID int64) (int, error) {
	chapters, err := a.Store.ListChapters(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	missing := a.MissingFiles(chapters)
	for _, c := range chapters {
		if !missing[c.ID] {
			continue
		}
		slog.Info("chapter file is gone", "chapter", c.Name, "path", c.FilePath)
		if err := a.Store.SetChapterState(ctx, c.ID, store.ChapterPending, "", "", 0); err != nil {
			return 0, err
		}
		a.Bus.Publish(jobs.Event{
			Kind: "chapter-updated", ChapterID: c.ID, SeriesID: seriesID,
			State: store.ChapterPending,
		})
	}
	return len(missing), nil
}

// plural renders a count with its noun. Kept here rather than imported from
// the web layer, which must not be a dependency of the job that runs this.
func plural(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// fetchPages downloads a chapter's images in order, honouring the image hooks
// the module implements.
//
// OnBeforeDownloadImage supplies request headers — usually the Referer an image
// host demands — and OnDownloadImage, when present, means the module fetches
// and transforms the image itself. Descrambling a tiled image happens there, so
// bypassing these hooks yields 403s or scrambled pages rather than an error.
func (a *App) fetchPages(ctx context.Context, r *scraper.Runner, ch store.Chapter, urls []string) ([]download.Page, error) {
	fetcher := download.NewFetcher(a.Limiter, a.Transport)
	fetcher.Referer = r.Module().RootURL
	moduleDownloads := r.HasHandler("OnDownloadImage")

	pages := make([]download.Page, 0, len(urls))
	for i, u := range urls {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

		// A module may hand back a page address that is not absolute: a bare
		// path, or a network-path reference like //cdn.example/p.jpg, which
		// is a perfectly ordinary URL that Go's client refuses because it
		// carries no scheme. Resolving against the site's own address is
		// what the browser these pages were written for would do.
		u = scraper.MaybeFillHost(r.Module().RootURL, u)

		headers, err := r.BeforeDownloadImage(u)
		if err != nil {
			return nil, fmt.Errorf("page %d of %d: %w", i+1, len(urls), err)
		}

		var page download.Page
		if moduleDownloads {
			data, err := r.DownloadImage(u)
			if err != nil {
				return nil, fmt.Errorf("page %d of %d: %w", i+1, len(urls), err)
			}
			// The module may have re-encoded the image, so the extension comes
			// from the bytes rather than the URL.
			page = download.Page{Data: data, Ext: download.ImageExt(data, u, "")}
		} else {
			page, err = fetcher.Page(ctx, u, headers)
			if err != nil {
				return nil, fmt.Errorf("page %d of %d (%s): %w", i+1, len(urls), u, err)
			}
		}

		if r.HasHandler("OnAfterImageSaved") {
			var err error
			if page, err = a.postProcessImage(r, page, i); err != nil {
				return nil, fmt.Errorf("page %d of %d: %w", i+1, len(urls), err)
			}
		}

		pages = append(pages, page)
		a.Bus.Publish(jobs.Event{
			Kind: "chapter-progress", ChapterID: ch.ID, SeriesID: ch.SeriesID,
			State: store.ChapterDownloading, Done: i + 1, Total: len(urls),
		})
	}
	return pages, nil
}

// postProcessImage hands a page to the module's OnAfterImageSaved handler.
//
// The handler expects a path, so the image is written to a temporary file and
// read back. This runs only for modules that declare the handler — one, at the
// time of writing — so the round trip costs nothing for everything else.
func (a *App) postProcessImage(r *scraper.Runner, page download.Page, index int) (download.Page, error) {
	dir, err := os.MkdirTemp("", "atsume-page-")
	if err != nil {
		return page, err
	}
	defer os.RemoveAll(dir)

	path := filepath.Join(dir, fmt.Sprintf("%04d%s", index+1, page.Ext))
	if err := os.WriteFile(path, page.Data, 0o644); err != nil {
		return page, err
	}
	if err := r.AfterImageSaved(path); err != nil {
		return page, err
	}

	// A handler may rename the file rather than rewrite it — the MangaFox
	// watermark remover writes a .png beside a .jpg and deletes the original
	// — so whatever is left in the directory is the result. Reading only the
	// path we wrote would silently discard the handler's work.
	written := path
	if _, err := os.Stat(path); err != nil {
		names, err := os.ReadDir(dir)
		if err != nil || len(names) == 0 {
			// A handler may also delete the file to drop the page; treat that
			// as "leave it alone" rather than failing the chapter.
			return page, nil
		}
		written = filepath.Join(dir, names[0].Name())
	}

	edited, err := os.ReadFile(written)
	if err != nil || len(edited) == 0 {
		return page, nil
	}
	page.Data = edited
	page.Ext = download.ImageExt(edited, "", "")
	return page, nil
}

// comicInfo describes a chapter for whatever reads the library directory.
//
// A file name cannot carry a title: the characters a filesystem forbids are
// stripped, so "1/2 Prince" becomes the folder "12 Prince" and a library
// server reading only the folder shows a different work. This says what the
// series actually is, in the site's own words.
func (a *App) comicInfo(ctx context.Context, series store.Series, target download.Chapter, root string, pages int) *download.ComicInfo {
	meta := download.SeriesMeta{
		Title:   series.Title,
		Summary: series.Summary,
		Authors: series.Authors,
		Artists: series.Artists,
		Genres:  series.Genres,
		Status:  series.Status,
		// Stored relative, as the site listed it. A note pointing at
		// "/manga/x/" is no use to whoever finds the file.
		URL:  scraper.MaybeFillHost(root, series.URL),
		Site: series.ModuleName,
	}
	// The chapter count is only worth stating for a finished work, and only
	// if it is known; a running total tells a reader their library is
	// incomplete when it is merely ongoing.
	if chapters, err := a.Store.ListChapters(ctx, series.ID); err == nil {
		meta.Chapters = len(chapters)
	}
	return download.BuildComicInfo(meta, target, pages)
}

// DownloadPayload identifies a chapter to download.
type DownloadPayload struct {
	ChapterID int64 `json:"chapter_id"`
}

// EnqueueDownload queues one chapter.
func (a *App) EnqueueDownload(ctx context.Context, chapterID int64) error {
	if err := a.Store.SetChapterState(ctx, chapterID, store.ChapterQueued, "", "", 0); err != nil {
		return err
	}
	if _, err := a.Queue.Enqueue(ctx, jobs.KindDownloadChapter, DownloadPayload{ChapterID: chapterID}); err != nil {
		return err
	}
	a.Pool.Notify()
	a.Bus.Publish(jobs.Event{Kind: "chapter-updated", ChapterID: chapterID, State: store.ChapterQueued})
	return nil
}

// EnqueueAllPending queues every chapter of a series that is not yet on disk.
func (a *App) EnqueueAllPending(ctx context.Context, seriesID int64) (int, error) {
	pending, err := a.Store.PendingChapters(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	// A chapter whose file has gone is outstanding again, whatever the
	// database says. Leaving it out meant "download everything waiting"
	// quietly skipped the chapters a reader had just deleted.
	all, err := a.Store.ListChapters(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	for id := range a.MissingFiles(all) {
		for _, c := range all {
			if c.ID == id {
				pending = append(pending, c)
			}
		}
	}
	for _, c := range pending {
		if err := a.EnqueueDownload(ctx, c.ID); err != nil {
			return 0, err
		}
	}
	return len(pending), nil
}

func (a *App) downloadChapter(ctx context.Context, raw json.RawMessage) error {
	var p DownloadPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	ch, err := a.Store.GetChapter(ctx, p.ChapterID)
	if err != nil {
		return err
	}
	series, err := a.Store.GetSeries(ctx, ch.SeriesID)
	if err != nil {
		return err
	}

	// Record the failure on the chapter as well as the job, so the UI can show
	// why a specific chapter is stuck without the operator reading logs.
	fail := func(err error) error {
		_ = a.Store.SetChapterState(ctx, ch.ID, store.ChapterFailed, "", err.Error(), 0)
		a.Bus.Publish(jobs.Event{
			Kind: "chapter-updated", ChapterID: ch.ID, SeriesID: ch.SeriesID,
			State: store.ChapterFailed, Message: err.Error(),
		})
		return err
	}

	if err := a.Store.SetChapterState(ctx, ch.ID, store.ChapterDownloading, "", "", 0); err != nil {
		return err
	}
	a.Bus.Publish(jobs.Event{
		Kind: "chapter-updated", ChapterID: ch.ID, SeriesID: ch.SeriesID,
		State: store.ChapterDownloading,
	})

	r, err := a.openModule(ctx, series.Key())
	if err != nil {
		return fail(err)
	}
	defer r.Close()

	pageURLs, err := r.GetPageNumber(ch.URL)
	if err != nil {
		return fail(err)
	}
	if len(pageURLs) == 0 {
		return fail(fmt.Errorf("module returned no pages for %s", ch.URL))
	}

	pages, err := a.fetchPages(ctx, r, ch, pageURLs)
	if err != nil {
		return fail(err)
	}

	target := download.Chapter{
		Series: series.Title, Name: ch.Name, Number: ch.Number, Volume: ch.Volume,
		URL: scraper.MaybeFillHost(r.Module().RootURL, ch.URL),
	}
	path := target.Path(a.Cfg.LibraryDir)
	if err := download.WriteCBZ(path, pages, a.comicInfo(ctx, series, target, r.Module().RootURL, len(pages))); err != nil {
		return fail(err)
	}

	if err := a.Store.SetChapterState(ctx, ch.ID, store.ChapterDone, path, "", len(pages)); err != nil {
		return err
	}
	slog.Info("chapter written", "series", series.Title, "chapter", ch.Name,
		"pages", len(pages), "path", path)
	a.Bus.Publish(jobs.Event{
		Kind: "chapter-updated", ChapterID: ch.ID, SeriesID: ch.SeriesID,
		State: store.ChapterDone, Done: len(pages), Total: len(pages),
	})
	return nil
}

// Preview fetches a series' details without storing anything.
//
// A directory listing carries only names and links, so there is no way to tell
// two similarly titled series apart from it. This is the one request that
// answers "is this the one I mean", made deliberately for a single series
// rather than for every row of an index.
func (a *App) Preview(ctx context.Context, module, seriesURL string) (*scraper.MangaInfo, error) {
	r, err := a.openModule(ctx, module)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.GetInfo(seriesURL)
}

// Follow records a series and queues the fetch of its details.
//
// The row is written here, not by the job, so that a series exists in the
// library the instant it is followed. Opening the module costs a millisecond
// and runs no requests — Init() only declares — and it also means an unknown
// site fails on the click rather than silently later.
func (a *App) Follow(ctx context.Context, moduleKey, seriesURL, title string) (int64, error) {
	key := a.ResolveModule(ctx, moduleKey)
	r, err := a.openModuleRaw(ctx, key)
	if err != nil {
		return 0, err
	}
	mod := r.Module()
	moduleID, moduleName := mod.ID, mod.Name
	r.Close()

	if title == "" {
		title = seriesURL
	}
	id, _, err := a.Store.EnsureSeries(ctx, store.Series{
		ModuleID: moduleID, ModuleKey: key, ModuleName: moduleName,
		URL: seriesURL, Title: title,
	})
	if err != nil {
		return 0, err
	}

	if err := a.EnqueueRefresh(ctx, key, seriesURL); err != nil {
		return id, err
	}
	return id, nil
}

// MissingFiles reports which chapters atsume believes it downloaded but whose
// file is no longer on disk.
//
// The library directory belongs to whatever reads it, and things happen to it
// that atsume does not do: a file deleted through Komga, a volume that was
// not mounted, a tidy-up. Until this, the chapter went on saying "done" and
// the interface offered nothing, because a downloaded chapter was assumed to
// stay downloaded.
func (a *App) MissingFiles(chapters []store.Chapter) map[int64]bool {
	missing := map[int64]bool{}
	for _, c := range chapters {
		if c.State != store.ChapterDone {
			continue
		}
		// A done chapter with no recorded path predates the path being
		// recorded; there is nothing to check and nothing to claim.
		if c.FilePath == "" {
			continue
		}
		if _, err := os.Stat(c.FilePath); err != nil {
			missing[c.ID] = true
		}
	}
	return missing
}
