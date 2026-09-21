package app

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"
)

// Scheduler re-checks subscribed series for new chapters.
//
// It enqueues refresh jobs rather than scraping directly, so checks share the
// worker pool, the per-host limiter and the retry behaviour with everything
// else. A tick takes only a batch of the series that are due, which keeps a
// large library from flooding the queue at startup.
type Scheduler struct {
	app      *App
	interval time.Duration
	batch    int

	// now is overridable so tests need not wait out an interval.
	tick chan struct{}

	// lastSweep is when a sweep last completed, in Unix seconds. The library
	// reads it to say whether checking is actually happening: a scheduler
	// that has stopped looks exactly like one with nothing to do, and the
	// page would go on quietly claiming everything is up to date.
	lastSweep atomic.Int64
}

// NewScheduler builds a scheduler from the app's configuration.
func NewScheduler(a *App) *Scheduler {
	return &Scheduler{
		app:      a,
		interval: a.Cfg.CheckInterval,
		batch:    a.Cfg.CheckBatch,
		tick:     make(chan struct{}, 1),
	}
}

// LastSweep reports when a sweep last completed, and whether one ever has in
// this process.
func (s *Scheduler) LastSweep() (time.Time, bool) {
	sec := s.lastSweep.Load()
	if sec == 0 {
		return time.Time{}, false
	}
	return time.Unix(sec, 0), true
}

// Enabled reports whether automatic checking runs at all.
func (s *Scheduler) Enabled() bool { return s.interval > 0 }

// Interval is how often a subscribed series is re-checked.
func (s *Scheduler) Interval() time.Duration { return s.interval }

// CheckNow asks for a sweep without waiting for the next interval.
func (s *Scheduler) CheckNow() {
	select {
	case s.tick <- struct{}{}:
	default: // a sweep is already pending
	}
}

// Run sweeps until ctx is cancelled. It returns immediately when automatic
// checking is disabled, leaving CheckNow as the only trigger.
func (s *Scheduler) Run(ctx context.Context) {
	if s.interval <= 0 {
		slog.Info("automatic chapter checks disabled")
		<-ctx.Done()
		return
	}
	slog.Info("chapter checks scheduled", "every", s.interval, "batch", s.batch)

	// The first sweep runs shortly after start rather than immediately, so a
	// restart does not stampede every tracked site at once.
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-s.tick:
		case <-timer.C:
		}

		if n, err := s.sweep(ctx); err != nil {
			slog.Error("chapter check sweep", "err", err)
		} else {
			// Recorded on a sweep that found nothing to do as well: having
			// run and found nothing due is the healthy case, and it is the
			// running that the library is asking about.
			s.lastSweep.Store(time.Now().Unix())
			if n > 0 {
				slog.Info("queued chapter checks", "series", n)
			}
		}

		timer.Reset(s.interval)
	}
}

// sweep enqueues a refresh for each series that is due, returning how many.
func (s *Scheduler) sweep(ctx context.Context) (int, error) {
	due, err := s.app.Store.SeriesDueForCheck(ctx, s.interval, s.batch)
	if err != nil {
		return 0, err
	}
	for _, series := range due {
		if err := s.app.EnqueueRefresh(ctx, series.Key(), series.URL); err != nil {
			return 0, err
		}
	}
	return len(due), nil
}
