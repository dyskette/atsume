// Package app wires the scraper, queue and store together and implements the
// job handlers.
package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

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
	Limiter  *scraper.HostLimiter
	Pool     *jobs.Pool
}

// New builds an App from its dependencies.
func New(cfg *config.Config, st *store.Store, reg *scraper.Registry) *App {
	a := &App{
		Cfg:      cfg,
		Store:    st,
		Queue:    jobs.NewQueue(st.DB),
		Bus:      jobs.NewBus(),
		Registry: reg,
		Limiter:  scraper.NewHostLimiter(cfg.HostRPS, cfg.HostConcurrency),
	}
	a.Pool = &jobs.Pool{
		Queue:   a.Queue,
		Bus:     a.Bus,
		Workers: cfg.Workers,
		Handlers: map[string]jobs.Handler{
			jobs.KindRefreshSeries:   a.refreshSeries,
			jobs.KindDownloadChapter: a.downloadChapter,
		},
	}
	return a
}

// openModule loads a module into a fresh Lua state. Each call gets its own
// state because the module API is built on globals and a state is not safe for
// concurrent use.
func (a *App) openModule(ctx context.Context, name string) (*scraper.Runner, error) {
	info, ok := a.Registry.Find(name)
	if !ok {
		return nil, fmt.Errorf("no module named %q in revision %s", name, a.Registry.Ref())
	}
	return a.Registry.Host(a.Limiter).Open(ctx, info.File)
}

// Browse lists one page of a site's directory.
func (a *App) Browse(ctx context.Context, module string, page int) ([]scraper.Entry, error) {
	r, err := a.openModule(ctx, module)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return r.GetNameAndLink(page)
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
		ModuleID:   mod.ID,
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
	if err := a.Store.ReplaceChapters(ctx, id, chs); err != nil {
		return err
	}

	a.Bus.Publish(jobs.Event{
		Kind: "series-updated", SeriesID: id, State: "done",
		Message: fmt.Sprintf("%s: %d chapters", info.Title, len(chs)),
	})
	return nil
}

// fetchPages downloads a chapter's images in order, honouring the image hooks
// the module implements.
//
// OnBeforeDownloadImage supplies request headers — usually the Referer an image
// host demands — and OnDownloadImage, when present, means the module fetches
// and transforms the image itself. Descrambling a tiled image happens there, so
// bypassing these hooks yields 403s or scrambled pages rather than an error.
func (a *App) fetchPages(ctx context.Context, r *scraper.Runner, ch store.Chapter, urls []string) ([]download.Page, error) {
	fetcher := download.NewFetcher(a.Limiter)
	fetcher.Referer = r.Module().RootURL
	moduleDownloads := r.HasHandler("OnDownloadImage")

	pages := make([]download.Page, 0, len(urls))
	for i, u := range urls {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}

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

		pages = append(pages, page)
		a.Bus.Publish(jobs.Event{
			Kind: "chapter-progress", ChapterID: ch.ID, SeriesID: ch.SeriesID,
			State: store.ChapterDownloading, Done: i + 1, Total: len(urls),
		})
	}
	return pages, nil
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

	r, err := a.openModule(ctx, series.ModuleName)
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
	}
	path := target.Path(a.Cfg.LibraryDir)
	if err := download.WriteCBZ(path, pages); err != nil {
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
