package scraper

import (
	"context"
	"sync"

	"golang.org/x/time/rate"
)

// HostLimiter bounds both the rate and the concurrency of requests to each
// host independently.
//
// A run touches hundreds of unrelated sites, so a global limit would either
// throttle the whole queue to the slowest site's budget or hammer a small one.
// Per-host state is the only setting that means anything here.
type HostLimiter struct {
	rps         float64
	burst       int
	concurrency int

	mu    sync.Mutex
	hosts map[string]*hostState
}

type hostState struct {
	limiter *rate.Limiter
	slots   chan struct{}
}

// NewHostLimiter builds a limiter allowing rps requests per second and at most
// concurrency simultaneous requests to any one host.
func NewHostLimiter(rps float64, concurrency int) *HostLimiter {
	if concurrency < 1 {
		concurrency = 1
	}
	burst := concurrency
	return &HostLimiter{
		rps:         rps,
		burst:       burst,
		concurrency: concurrency,
		hosts:       map[string]*hostState{},
	}
}

func (h *HostLimiter) state(host string) *hostState {
	h.mu.Lock()
	defer h.mu.Unlock()
	st, ok := h.hosts[host]
	if !ok {
		st = &hostState{
			limiter: rate.NewLimiter(rate.Limit(h.rps), h.burst),
			slots:   make(chan struct{}, h.concurrency),
		}
		h.hosts[host] = st
	}
	return st
}

// Wait blocks until a request to host may proceed.
//
// The concurrency slot is released immediately rather than held for the
// request's duration: the rate limiter already spaces requests out, and holding
// the slot across a slow response would serialise an entire site behind one
// stalled connection.
func (h *HostLimiter) Wait(ctx context.Context, host string) error {
	st := h.state(host)
	select {
	case st.slots <- struct{}{}:
		defer func() { <-st.slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	return st.limiter.Wait(ctx)
}
