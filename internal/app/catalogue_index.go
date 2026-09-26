package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/prebuilt"
	"github.com/dyskette/atsume/internal/store"
)

// IndexPayload names a site to read, and where from.
type IndexPayload struct {
	Site string `json:"site"`
	// Source is empty to take whichever is quicker, or "site" to insist on
	// reading the site itself. A reader who presses "read again" while
	// looking at a snapshot from 2024 wants the site, not the snapshot.
	Source string `json:"source,omitempty"`
}

// indexBatch is how many titles are written at a time while a read runs.
// Small enough that a reader watching a slow site sees it fill, large enough
// that a catalogue of thousands is not thousands of transactions.
const indexBatch = 200

// maxIndexPositions bounds one read of a site.
//
// A site is paged until it stops returning titles, and a module that
// miscounts its own pagination would otherwise page forever. Ten thousand
// positions is far past any real catalogue and still terminates.
const maxIndexPositions = 10000

// EnqueueIndex queues a read of a site's catalogue, unless one is already
// waiting.
//
// Reading a catalogue can take minutes — MangaToon's module pages the site
// internally and hands back 2,677 titles after three of them — so it cannot
// happen inside a request. Holding an HTTP connection open that long is its
// own failure: the browser or the proxy in front gives up first and the work
// is lost anyway.
func (a *App) EnqueueIndex(ctx context.Context, site, source string) error {
	site = a.ResolveModule(ctx, site)
	if a.Indexing(ctx, site) {
		return nil
	}
	if _, err := a.Queue.Enqueue(ctx, jobs.KindIndexSite,
		IndexPayload{Site: site, Source: source}); err != nil {
		return err
	}
	a.Pool.Notify()
	return nil
}

// Indexing reports whether a read of this site is queued or running, so
// pressing refresh twice does not read the site twice.
func (a *App) Indexing(ctx context.Context, site string) bool {
	pending, err := a.Queue.PendingOfKind(ctx, jobs.KindIndexSite)
	if err != nil {
		return false
	}
	for _, raw := range pending {
		var p IndexPayload
		if json.Unmarshal(raw, &p) == nil && p.Site == site {
			return true
		}
	}
	return false
}

// indexSite reads every title a site lists and keeps them.
func (a *App) indexSite(ctx context.Context, raw json.RawMessage) error {
	var p IndexPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}

	startedAt := time.Now()
	read, err := a.Store.BeginSiteCatalogue(ctx, p.Site)
	if err != nil {
		return err
	}
	a.publishIndex(p.Site, "working", "Reading the catalogue…", 0)

	// A published snapshot arrives in seconds where reading the site takes
	// minutes, so it is tried first unless the reader asked for the site.
	// It may be years old; saying so is the interface's job, not a reason to
	// make everyone wait.
	if p.Source != store.SourceSite {
		switch done, err := a.indexFromSnapshot(ctx, p.Site, read, startedAt); {
		case err == nil && done:
			return nil
		case err != nil:
			slog.Info("no usable snapshot; reading the site instead",
				"site", p.Site, "err", err)
		}
	}

	var (
		seq   int
		batch []store.SiteTitle
		at    BrowsePos
		// Seen decides when to stop. Asking "did this position return
		// anything" is not enough: MangaToon's module ignores the page it is
		// given and walks the site's own paging internally, so every
		// position hands back the same 2,677 titles and a read that trusted
		// the count would never finish.
		seen = map[string]bool{}
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := a.Store.AddSiteTitles(ctx, p.Site, read, batch); err != nil {
			return err
		}
		batch = batch[:0]
		a.publishIndex(p.Site, "working",
			fmt.Sprintf("Reading the catalogue — %s so far, %s elapsed",
				plural(seq, "title"), humanElapsed(time.Since(startedAt))), seq)
		return nil
	}

	for step := 0; step < maxIndexPositions; step++ {
		res, err := a.Browse(ctx, p.Site, at)
		if err != nil {
			// Whatever was read stays: a partial catalogue a reader can
			// search beats nothing, as long as it says it is partial.
			_ = flush()
			_ = a.Store.FinishSiteCatalogue(ctx, p.Site, false, err.Error(),
				store.SourceSite, time.Time{}, read)
			a.publishIndex(p.Site, "failed", err.Error(), seq)
			return err
		}
		var added int
		for _, e := range res.Entries {
			if e.Link == "" || seen[e.Link] {
				continue
			}
			seen[e.Link] = true
			added++
			batch = append(batch, store.SiteTitle{URL: e.Link, Name: e.Name, Seq: seq})
			seq++
			if len(batch) >= indexBatch {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		// A position that adds nothing new is the end, whether the site ran
		// out or the module is handing back the same list each time.
		if added == 0 || !res.More {
			break
		}
		at = res.Next
	}
	if err := flush(); err != nil {
		return err
	}

	note := fmt.Sprintf("%s in %s", plural(seq, "title"), humanElapsed(time.Since(startedAt)))
	complete, source, dataAt := true, store.SourceSite, time.Now()
	// A read that finds nothing on a site that listed titles before is more
	// likely a changed layout, or a challenge page served as 200, than a
	// site that emptied overnight. Finishing it as complete would remove
	// every stored title, so the list is kept, still saying where it came
	// from and how old it is, and the read is marked incomplete.
	if seq == 0 {
		if before, err := a.Store.SiteCatalogueInfo(ctx, p.Site); err == nil && before.Titles > 0 {
			complete, source, dataAt = false, before.Source, before.DataAt
			note = fmt.Sprintf("the site listed no titles, so the %s from before were kept",
				plural(before.Titles, "title"))
		}
	}
	if err := a.Store.FinishSiteCatalogue(ctx, p.Site, complete, note,
		source, dataAt, read); err != nil {
		return err
	}
	slog.Info("site catalogue read", "site", p.Site, "titles", seq, "took", time.Since(startedAt))
	a.publishIndex(p.Site, "done", note, seq)
	return nil
}

// publishIndex tells anyone watching the site page how the read is going.
func (a *App) publishIndex(site, state, message string, titles int) {
	a.Bus.Publish(jobs.Event{
		Kind: "site-indexed", State: state, Message: message,
		Site: site, Done: titles,
	})
}

// humanElapsed renders a duration the way someone waiting reads it.
func humanElapsed(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	default:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
}

// indexFromSnapshot fills a site's catalogue from a published snapshot,
// reporting whether it managed to.
//
// Nearly half the sites have none, and a snapshot that is missing, broken or
// for a site whose module declares no id is an ordinary outcome rather than
// a failure: the caller reads the site instead.
func (a *App) indexFromSnapshot(ctx context.Context, site string, read int64, startedAt time.Time) (bool, error) {
	if a.Snapshots == nil {
		return false, prebuilt.ErrNoSnapshot
	}
	e, ok := a.SiteInfo(ctx, site)
	if !ok || e.ID == "" {
		return false, prebuilt.ErrNoSnapshot
	}

	a.publishIndex(site, "working", "Looking for a published catalogue…", 0)
	snap, err := a.Snapshots.Fetch(ctx, e.ID)
	if err != nil {
		return false, err
	}

	titles := make([]store.SiteTitle, 0, len(snap.Titles))
	for i, t := range snap.Titles {
		titles = append(titles, store.SiteTitle{URL: t.URL, Name: t.Name, Seq: i})
	}
	for start := 0; start < len(titles); start += indexBatch {
		end := min(start+indexBatch, len(titles))
		if err := a.Store.AddSiteTitles(ctx, site, read, titles[start:end]); err != nil {
			return false, err
		}
	}

	note := fmt.Sprintf("%s from a published catalogue, %s", plural(len(titles), "title"),
		humanBytes(snap.Bytes))
	if err := a.Store.FinishSiteCatalogue(ctx, site, true, note,
		store.SourcePrebuilt, snap.Newest, read); err != nil {
		return false, err
	}
	slog.Info("site catalogue from snapshot", "site", site, "titles", len(titles),
		"newest", snap.Newest.Format(time.DateOnly), "bytes", snap.Bytes,
		"took", time.Since(startedAt))
	a.publishIndex(site, "done", note, len(titles))
	return true, nil
}

// humanBytes renders a download size the way someone deciding about it reads
// it.
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n/(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}
