package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/store"
)

// errCancelled is the cause a running download is stopped with, which tells
// the download it was cancelled rather than failed.
var errCancelled = errors.New("download cancelled")

// DownloadProgress is how far along a running chapter download is. Total is
// 0 while the chapter's page list is still being fetched.
type DownloadProgress struct {
	Done, Total int
}

// activeDownloads tracks the chapter downloads running now: how to stop each
// one, and how many of its pages are in.
type activeDownloads struct {
	mu sync.Mutex
	m  map[int64]*activeDownload
}

type activeDownload struct {
	cancel context.CancelCauseFunc
	DownloadProgress
}

func (d *activeDownloads) start(id int64, cancel context.CancelCauseFunc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.m == nil {
		d.m = map[int64]*activeDownload{}
	}
	d.m[id] = &activeDownload{cancel: cancel}
}

func (d *activeDownloads) progress(id int64, done, total int) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if a := d.m[id]; a != nil {
		a.Done, a.Total = done, total
	}
}

func (d *activeDownloads) finish(id int64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.m, id)
}

// stop cancels the download of chapter id if one is running.
func (d *activeDownloads) stop(id int64) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	a := d.m[id]
	if a == nil {
		return false
	}
	a.cancel(errCancelled)
	return true
}

// ActiveDownloads reports the chapter downloads running now, by chapter ID.
func (a *App) ActiveDownloads() map[int64]DownloadProgress {
	a.active.mu.Lock()
	defer a.active.mu.Unlock()
	out := make(map[int64]DownloadProgress, len(a.active.m))
	for id, d := range a.active.m {
		out[id] = d.DownloadProgress
	}
	return out
}

// CancelChapter stops a chapter being downloaded, or takes it out of the
// queue, and returns it to not downloaded. A running download is interrupted
// and its pages, still in memory, are dropped; nothing reaches the library.
func (a *App) CancelChapter(ctx context.Context, id int64) error {
	if a.active.stop(id) {
		return nil // the download itself resets the chapter as it returns
	}
	n, err := a.Queue.DeletePending(ctx, jobs.KindDownloadChapter, "chapter_id", id)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("chapter %d is not queued or downloading", id)
	}
	return a.resetChapter(ctx, id)
}

// resetChapter returns a chapter to not downloaded and tells the pages
// showing it.
func (a *App) resetChapter(ctx context.Context, id int64) error {
	if err := a.Store.SetChapterState(ctx, id, store.ChapterPending, "", "", 0); err != nil {
		return err
	}
	a.Bus.Publish(jobs.Event{Kind: "chapter-updated", ChapterID: id, State: store.ChapterPending})
	return nil
}

// QueuePositions gives each queued chapter its place in line, 1 being the
// next to start.
func (a *App) QueuePositions(ctx context.Context) (map[int64]int, error) {
	payloads, err := a.Queue.PendingInOrder(ctx, jobs.KindDownloadChapter)
	if err != nil {
		return nil, err
	}
	out := make(map[int64]int, len(payloads))
	for _, raw := range payloads {
		var p DownloadPayload
		if json.Unmarshal(raw, &p) == nil && out[p.ChapterID] == 0 {
			out[p.ChapterID] = len(out) + 1
		}
	}
	return out, nil
}

// PauseQueue stops new downloads and checks from starting; running ones
// finish. ResumeQueue undoes it, and so does a restart.
func (a *App) PauseQueue() { a.Pool.Pause() }

// ResumeQueue lets queued work start again.
func (a *App) ResumeQueue() { a.Pool.Resume() }

// QueuePaused reports whether the queue is paused.
func (a *App) QueuePaused() bool { return a.Pool.Paused() }
