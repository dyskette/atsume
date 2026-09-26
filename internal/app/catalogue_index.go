package app

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
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
	// Resume carries on an unfinished read from where it stopped instead of
	// starting over.
	Resume bool `json:"resume,omitempty"`
	// Retry is how many automatic retries came before this one.
	Retry int `json:"retry,omitempty"`
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

// retrySchedule is how long atsume waits before reading a site again by
// itself after it was down or unreachable. A site that is down is usually
// back within the hour; one that is not after the last try is left for the
// reader to retry. Other failures are not retried: a site that refuses
// atsume or has moved will do the same in five minutes.
var retrySchedule = []time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour}

// EnqueueIndex queues a read of a site's catalogue, unless one is already
// running or about to.
//
// Reading a catalogue can take minutes — MangaToon's module pages the site
// internally and hands back 2,677 titles after three of them — so it cannot
// happen inside a request. Holding an HTTP connection open that long is its
// own failure: the browser or the proxy in front gives up first and the work
// is lost anyway.
func (a *App) EnqueueIndex(ctx context.Context, site, source string) error {
	return a.enqueueIndex(ctx, IndexPayload{Site: a.ResolveModule(ctx, site), Source: source})
}

// EnqueueResume queues an unfinished read of a site to carry on from where
// it stopped.
func (a *App) EnqueueResume(ctx context.Context, site string) error {
	return a.enqueueIndex(ctx, IndexPayload{Site: a.ResolveModule(ctx, site), Source: store.SourceSite, Resume: true})
}

func (a *App) enqueueIndex(ctx context.Context, p IndexPayload) error {
	if a.Indexing(ctx, p.Site) {
		return nil
	}
	// A reader asking now replaces an automatic retry waiting for later.
	if _, err := a.Queue.DeletePending(ctx, jobs.KindIndexSite, "site", p.Site); err != nil {
		return err
	}
	if _, err := a.Queue.Enqueue(ctx, jobs.KindIndexSite, p); err != nil {
		return err
	}
	a.Pool.Notify()
	return nil
}

// Indexing reports whether a read of this site is running or about to, so
// pressing refresh twice does not read the site twice. A retry waiting for
// later does not count: until it starts, nothing is being read.
func (a *App) Indexing(ctx context.Context, site string) bool {
	if _, ok := a.ActiveReads()[site]; ok {
		return true
	}
	active, err := a.Queue.ActiveOfKind(ctx, jobs.KindIndexSite)
	if err != nil {
		return false
	}
	for _, raw := range active {
		var p IndexPayload
		if json.Unmarshal(raw, &p) == nil && p.Site == site {
			return true
		}
	}
	return false
}

// indexSite reads every title a site lists and keeps them.
//
// A read that fails or is stopped keeps what it read and where it stopped,
// so it can be carried on. It completes its job either way: retrying is
// decided here, for the failures a retry can help, rather than by the queue.
func (a *App) indexSite(jobCtx context.Context, raw json.RawMessage) error {
	var p IndexPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return err
	}
	startedAt := time.Now()
	before, err := a.Store.SiteCatalogueInfo(jobCtx, p.Site)
	if err != nil {
		return err
	}

	var (
		read  int64
		at    store.ReadPos
		steps int
		// Seen decides when to stop. Asking "did this position return
		// anything" is not enough: MangaToon's module ignores the page it is
		// given and walks the site's own paging internally, so every
		// position hands back the same 2,677 titles and a read that trusted
		// the count would never finish.
		seen    = map[string]bool{}
		resumed bool
	)
	if p.Resume {
		var urls []string
		read, at, steps, urls, resumed, err = a.Store.ResumeSiteCatalogue(jobCtx, p.Site)
		if err != nil {
			return err
		}
		for _, u := range urls {
			seen[u] = true
		}
	}
	if !resumed {
		if read, err = a.Store.BeginSiteCatalogue(jobCtx, p.Site); err != nil {
			return err
		}
		at, steps = store.ReadPos{}, 0
	}

	// Stopping cancels this context; bookkeeping afterwards uses the job's.
	ctx, cancel := context.WithCancelCause(jobCtx)
	defer cancel(nil)
	progress := ReadProgress{Page: steps + 1, Titles: len(seen)}
	a.reads.start(p.Site, cancel, progress)
	defer a.reads.finish(p.Site)
	a.publishIndex(p.Site, "working", "Reading the catalogue…", progress)

	// A published snapshot arrives in seconds where reading the site takes
	// minutes, so it is tried first unless the reader asked for the site.
	// It may be years old; saying so is the interface's job, not a reason to
	// make everyone wait.
	if !resumed && p.Source != store.SourceSite {
		switch done, err := a.indexFromSnapshot(ctx, p.Site, read, startedAt); {
		case err == nil && done:
			return nil
		case err != nil:
			slog.Info("no usable snapshot; reading the site instead",
				"site", p.Site, "err", err)
		}
	}
	progress.Estimate = a.readEstimate(ctx, p.Site, before)
	a.reads.progress(p.Site, progress)

	var (
		batch []store.SiteTitle
		// Challenged is whether any page was an anti-bot interstitial,
		// which a module can take for an empty page without complaint.
		challenged bool
		movedTo    string
	)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := a.Store.AddSiteTitles(jobCtx, p.Site, read, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}

	for ; steps < maxIndexPositions; steps++ {
		progress.Page = steps + 1
		a.reads.progress(p.Site, progress)
		a.publishIndex(p.Site, "working", fmt.Sprintf("Reading the catalogue — %s so far, %s elapsed",
			plural(progress.Titles, "title"), humanElapsed(time.Since(startedAt))), progress)

		res, err := a.Browse(ctx, p.Site, BrowsePos{Dir: at.Dir, Page: at.Page})
		if err != nil {
			// Whatever was read stays: a partial catalogue a reader can
			// search beats nothing, as long as it says it is partial.
			if ferr := flush(); ferr != nil {
				return ferr
			}
			return a.readFailed(jobCtx, ctx, p, read, at, steps, cmp.Or(res.MovedTo, movedTo), err)
		}
		challenged = challenged || res.Challenged
		movedTo = cmp.Or(movedTo, res.MovedTo)
		var added int
		for _, e := range res.Entries {
			if e.Link == "" || seen[e.Link] {
				continue
			}
			seen[e.Link] = true
			added++
			batch = append(batch, store.SiteTitle{URL: e.Link, Name: e.Name, Seq: len(seen) - 1})
			if len(batch) >= indexBatch {
				if err := flush(); err != nil {
					return err
				}
			}
		}
		progress.Titles = len(seen)
		// A position that adds nothing new is the end, whether the site ran
		// out or the module is handing back the same list each time.
		if added == 0 || !res.More {
			steps++
			break
		}
		at = store.ReadPos{Dir: res.Next.Dir, Page: res.Next.Page}
	}
	if err := flush(); err != nil {
		return err
	}

	titles := len(seen)
	note := fmt.Sprintf("%s in %s", plural(titles, "title"), humanElapsed(time.Since(startedAt)))
	out := store.ReadOutcome{
		Complete: true, Note: note, Source: store.SourceSite, DataAt: time.Now(), Steps: steps,
		Resume: store.NoPos, Failure: store.Failure{Challenged: challenged, MovedTo: movedTo},
	}
	// A read that finds nothing is more likely a changed layout, or a
	// challenge page served as 200, than a site that emptied overnight.
	// Finishing it as complete would remove every stored title, so a list
	// already here is kept, still saying where it came from and how old it
	// is, and the read is marked incomplete.
	if titles == 0 {
		if challenged {
			out.Complete, out.Problem = false, ProblemBlocked
		}
		if before.Titles > 0 {
			out.Complete, out.Source, out.DataAt = false, before.Source, before.DataAt
			out.Note = fmt.Sprintf("the site listed no titles, so the %s from before were kept",
				plural(before.Titles, "title"))
		}
	}
	if err := a.Store.FinishSiteCatalogue(jobCtx, p.Site, read, out); err != nil {
		return err
	}
	slog.Info("site catalogue read", "site", p.Site, "titles", titles, "took", time.Since(startedAt))
	a.publishIndex(p.Site, "done", out.Note, progress)
	return nil
}

// readFailed records a read that ended early, keeping where it stopped, and
// schedules a retry when the site was down or unreachable.
func (a *App) readFailed(jobCtx, ctx context.Context, p IndexPayload, read int64, at store.ReadPos, steps int, movedTo string, err error) error {
	// A shutdown is not the site's fault; the job runs again at start-up.
	if jobCtx.Err() != nil {
		return err
	}
	out := store.ReadOutcome{
		Note: err.Error(), Source: store.SourceSite, Steps: steps, Resume: at,
		Problem: classifyProblem(err), Retries: p.Retry, Failure: failureOf(err),
	}
	out.MovedTo = movedTo
	state := "failed"
	if errors.Is(context.Cause(ctx), errStopped) {
		out.Note, out.Problem, state = "stopped by the reader", ProblemStopped, "stopped"
	}
	if (out.Problem == ProblemDown || out.Problem == ProblemUnreachable) && p.Retry < len(retrySchedule) {
		out.RetryAt = time.Now().Add(retrySchedule[p.Retry])
		out.Retries = p.Retry + 1
		retry := IndexPayload{Site: p.Site, Source: store.SourceSite, Resume: true, Retry: p.Retry + 1}
		if _, qerr := a.Queue.EnqueueAt(jobCtx, jobs.KindIndexSite, retry, out.RetryAt); qerr != nil {
			return qerr
		}
	}
	if ferr := a.Store.FinishSiteCatalogue(jobCtx, p.Site, read, out); ferr != nil {
		return ferr
	}
	slog.Info("site catalogue read ended early", "site", p.Site, "problem", out.Problem, "err", err)
	a.publishIndex(p.Site, state, out.Note, ReadProgress{Page: steps + 1})
	return nil
}

// readEstimate says how many directory pages a read will probably take: the
// module's own count when it gives one for a site in a single section,
// otherwise how many the last complete read took, otherwise 0.
func (a *App) readEstimate(ctx context.Context, site string, before store.SiteCatalogue) int {
	if r, err := a.openModule(ctx, site); err == nil {
		defer r.Close()
		if r.TotalDirectories() == 1 {
			if n, err := r.GetDirectoryPageNumber(); err == nil && n > 1 {
				return n
			}
		}
	}
	return before.Pages
}

// publishIndex tells anyone watching the site page how the read is going.
// A read that has ended leaves the running reads first, so what the event
// triggers — the footer among it — no longer counts it.
func (a *App) publishIndex(site, state, message string, p ReadProgress) {
	if state != "working" {
		a.reads.finish(site)
	}
	a.Bus.Publish(jobs.Event{
		Kind: "site-indexed", State: state, Message: message,
		Site: site, Done: p.Page, Total: p.Estimate,
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

	a.publishIndex(site, "working", "Looking for a published catalogue…", ReadProgress{})
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
	if err := a.Store.FinishSiteCatalogue(ctx, site, read, store.ReadOutcome{
		Complete: true, Note: note, Source: store.SourcePrebuilt, DataAt: snap.Newest,
	}); err != nil {
		return false, err
	}
	slog.Info("site catalogue from snapshot", "site", site, "titles", len(titles),
		"newest", snap.Newest.Format(time.DateOnly), "bytes", snap.Bytes,
		"took", time.Since(startedAt))
	a.publishIndex(site, "done", note, ReadProgress{})
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
