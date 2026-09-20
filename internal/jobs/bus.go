// Package jobs runs background work off a SQLite-backed queue and publishes
// progress to connected browsers.
package jobs

import "sync"

// Event is a progress update broadcast to the UI.
type Event struct {
	// Kind names the SSE event, which htmx matches with sse-swap.
	Kind string
	// ChapterID and SeriesID identify what changed, so a listener can decide
	// whether the update is relevant to the page it is showing.
	ChapterID int64
	SeriesID  int64
	State     string
	Message   string
	Done      int
	Total     int
}

// Bus fans events out to every connected subscriber.
//
// It is deliberately in-process: a single atsume instance owns its downloads,
// and introducing a broker would buy nothing but operational surface.
type Bus struct {
	mu   sync.RWMutex
	subs map[chan Event]struct{}
}

// NewBus returns an empty bus.
func NewBus() *Bus { return &Bus{subs: map[chan Event]struct{}{}} }

// Subscribe registers a listener and returns it with its cancel function.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	// Buffered so that one slow browser cannot stall a worker.
	ch := make(chan Event, 64)

	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
}

// Publish delivers an event to every subscriber, dropping it for any listener
// whose buffer is full rather than blocking the caller.
func (b *Bus) Publish(e Event) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default:
		}
	}
}
