package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Handler runs one job. Returning an error reschedules the job with backoff
// until its attempts are exhausted.
type Handler func(ctx context.Context, payload json.RawMessage) error

// Pool runs queued jobs across a fixed number of workers.
type Pool struct {
	Queue    *Queue
	Bus      *Bus
	Handlers map[string]Handler
	// Workers is how many jobs run at once.
	Workers int
	// Idle is how long to wait before polling again when the queue is empty.
	// Enqueue also signals the pool directly, so this is only a safety net.
	Idle time.Duration

	wake   chan struct{}
	once   sync.Once
	paused atomic.Bool
}

// Notify wakes an idle worker, so a job enqueued from a web request starts
// immediately instead of waiting out the poll interval.
func (p *Pool) Notify() {
	p.init()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *Pool) init() {
	p.once.Do(func() {
		if p.Idle == 0 {
			p.Idle = 5 * time.Second
		}
		if p.Workers < 1 {
			p.Workers = 1
		}
		// Room for one signal per worker, so Resume can wake them all.
		p.wake = make(chan struct{}, p.Workers)
	})
}

// Pause stops workers taking new jobs. Jobs already running finish; queued
// ones wait. It lasts until Resume or a restart.
func (p *Pool) Pause() { p.paused.Store(true) }

// Resume lets workers take jobs again and wakes them at once.
func (p *Pool) Resume() {
	p.init()
	p.paused.Store(false)
	for range p.Workers {
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// Paused reports whether the pool is paused.
func (p *Pool) Paused() bool { return p.paused.Load() }

// Run starts the workers and blocks until ctx is cancelled.
func (p *Pool) Run(ctx context.Context) {
	p.init()

	if n, err := p.Queue.ResetRunning(ctx); err != nil {
		slog.Error("could not requeue abandoned jobs", "err", err)
	} else if n > 0 {
		slog.Warn("requeued jobs abandoned by a previous run", "count", n)
	}

	var wg sync.WaitGroup
	for i := 0; i < p.Workers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			p.work(ctx, id)
		}(i)
	}
	wg.Wait()
}

func (p *Pool) work(ctx context.Context, id int) {
	for {
		if ctx.Err() != nil {
			return
		}
		var job *Job
		if !p.paused.Load() {
			var err error
			if job, err = p.Queue.Claim(ctx); err != nil {
				slog.Error("claim job", "worker", id, "err", err)
			}
		}
		if job == nil {
			select {
			case <-ctx.Done():
				return
			case <-p.wake:
			case <-time.After(p.Idle):
			}
			continue
		}
		p.run(ctx, job)
	}
}

func (p *Pool) run(ctx context.Context, job *Job) {
	h, ok := p.Handlers[job.Kind]
	if !ok {
		err := fmt.Errorf("no handler registered for job kind %q", job.Kind)
		slog.Error("run job", "id", job.ID, "err", err)
		// An unknown kind will never succeed, so burn the attempts immediately.
		job.Attempts = job.MaxAttempts
		_ = p.Queue.Fail(ctx, job, err)
		return
	}

	start := time.Now()
	if err := h(ctx, job.Payload); err != nil {
		slog.Warn("job failed", "id", job.ID, "kind", job.Kind,
			"attempt", job.Attempts, "err", err)
		if ferr := p.Queue.Fail(ctx, job, err); ferr != nil {
			slog.Error("record job failure", "id", job.ID, "err", ferr)
		}
		return
	}
	if err := p.Queue.Complete(ctx, job.ID); err != nil {
		slog.Error("complete job", "id", job.ID, "err", err)
	}
	slog.Info("job done", "id", job.ID, "kind", job.Kind, "took", time.Since(start))
}
