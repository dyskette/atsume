package app

import (
	"context"
	"errors"
	"sync"
)

// errStopped is the cause a site read is cancelled with when the reader
// stops it, which tells it apart from a shutdown or a failure.
var errStopped = errors.New("read stopped")

// ReadProgress is how far along a running site read is.
type ReadProgress struct {
	// Page is the directory page being read, 1-based.
	Page int
	// Estimate is how many pages the read will probably take, 0 when
	// nothing says.
	Estimate int
	// Titles is how many titles the read has found so far.
	Titles int
}

// activeReads tracks the site reads running now: how to stop each, and how
// far it has got.
type activeReads struct {
	mu sync.Mutex
	m  map[string]*activeRead
}

type activeRead struct {
	cancel context.CancelCauseFunc
	ReadProgress
}

func (r *activeReads) start(site string, cancel context.CancelCauseFunc, p ReadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.m == nil {
		r.m = map[string]*activeRead{}
	}
	r.m[site] = &activeRead{cancel: cancel, ReadProgress: p}
}

func (r *activeReads) progress(site string, p ReadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if a := r.m[site]; a != nil {
		a.ReadProgress = p
	}
}

func (r *activeReads) finish(site string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.m, site)
}

func (r *activeReads) stop(site string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.m[site]
	if a == nil {
		return false
	}
	a.cancel(errStopped)
	return true
}

// ActiveReads reports the site reads running now, by site.
func (a *App) ActiveReads() map[string]ReadProgress {
	a.reads.mu.Lock()
	defer a.reads.mu.Unlock()
	out := make(map[string]ReadProgress, len(a.reads.m))
	for site, r := range a.reads.m {
		out[site] = r.ReadProgress
	}
	return out
}

// StopRead stops a running read of a site, keeping what it read so far as an
// unfinished read that can be carried on. It reports whether one was running.
func (a *App) StopRead(site string) bool { return a.reads.stop(site) }
