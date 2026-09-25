package web

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/a-h/templ"
	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/store"
	"github.com/dyskette/atsume/internal/web/ui"
)

// statusInterval throttles the footer. A chapter emits one event per page, and
// the status line does not need to keep up with that.
const statusInterval = 500 * time.Millisecond

// handleEvents streams progress to the browser as server-sent events.
//
// Every message carries rendered HTML rather than JSON: htmx swaps it straight
// into the page, so there is no client-side state to keep in sync with the
// server's. The event name matches the sse-swap attribute on the target
// element, which is how a message reaches one specific chapter row.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Without this an intermediate proxy may buffer the stream into silence.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	events, cancel := s.App.Bus.Subscribe()
	defer cancel()

	var lastStatus time.Time

	// A periodic comment keeps idle connections alive through proxies that drop
	// quiet ones.
	ping := time.NewTicker(25 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return

		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()

		case e, open := <-events:
			if !open {
				return
			}
			for _, ev := range s.renderEvent(r.Context(), e) {
				writeEvent(w, ev.name, ev.html)
			}
			// The status line is derived here rather than published by the
			// worker: every event that matters to it is already on this
			// stream. Only page progress is throttled, since recomputing once
			// per page would be a query per image; a chapter changing state
			// always refreshes it, or a download finishing while the queue is
			// paused would leave the footer saying it is still running.
			if e.Kind != "chapter-progress" || time.Since(lastStatus) > statusInterval {
				lastStatus = time.Now()
				writeEvent(w, "queue", renderToString(r.Context(), ui.QueueStatus(s.queueView(r.Context()))))
			}
			flusher.Flush()
		}
	}
}

// sseEvent is one event for the browser: the name an element swaps on, and
// the HTML it swaps in.
type sseEvent struct{ name, html string }

// renderEvent turns a bus event into the events that update the page.
func (s *Server) renderEvent(ctx context.Context, e jobs.Event) []sseEvent {
	switch e.Kind {
	case "chapter-progress":
		ch, err := s.App.Store.GetChapter(ctx, e.ChapterID)
		if err != nil {
			return nil
		}
		st := ui.RowState{Active: true, Done: e.Done, Total: e.Total}
		return []sseEvent{{fmt.Sprintf("chapter-%d", e.ChapterID), renderToString(ctx, ui.ChapterRowBody(ch, false, st))}}

	case "chapter-updated":
		ch, err := s.App.Store.GetChapter(ctx, e.ChapterID)
		if err != nil {
			return nil
		}
		out := []sseEvent{{fmt.Sprintf("chapter-%d", e.ChapterID), renderToString(ctx, ui.ChapterRowBody(ch, false, s.rowState(ctx, ch)))}}
		// A chapter leaving the queue, by starting or being cancelled, moves
		// every one behind it up a place, so the rows that show their place
		// are sent again.
		if ch.State == store.ChapterDownloading || ch.State == store.ChapterPending {
			out = append(out, s.queuedRows(ctx)...)
		}
		return out

	case "series-updated", "queue-updated":
		// The footer replaces its whole element with a "queue" event, so this
		// sends the footer itself, refreshed now that a check has finished and
		// may have queued downloads, or the queue was paused or resumed. Plain
		// text here would remove the element and stop the footer updating
		// until the page was reloaded.
		return []sseEvent{{"queue", renderToString(ctx, ui.QueueStatus(s.queueView(ctx)))}}

	case "site-indexed":
		// The status line reports itself while a read runs, so a reader
		// watching a slow site sees numbers move rather than a spinner that
		// stopped meaning anything after three seconds.
		info, err := s.App.Store.SiteCatalogueInfo(ctx, e.Site)
		if err != nil {
			return nil
		}
		info.Note = e.Message
		v := ui.BrowseView{
			Module:    e.Site,
			Catalogue: info,
			Indexing:  e.State == "working",
		}
		return []sseEvent{{"site-" + e.Site, renderToString(ctx, ui.CatalogueStatus(v))}}
	}
	return nil
}

// queuedRows renders the queued chapters near enough the front of the queue
// to show their place in it.
func (s *Server) queuedRows(ctx context.Context) []sseEvent {
	positions, err := s.App.QueuePositions(ctx)
	if err != nil {
		return nil
	}
	var out []sseEvent
	for id, p := range positions {
		if !ui.NumberedPosition(p) {
			continue // said plain "Queued" before and still does
		}
		ch, err := s.App.Store.GetChapter(ctx, id)
		if err != nil || ch.State != store.ChapterQueued {
			continue
		}
		out = append(out, sseEvent{fmt.Sprintf("chapter-%d", id), renderToString(ctx, ui.ChapterRowBody(ch, false, ui.RowState{Position: p}))})
	}
	return out
}

// renderToString renders a component into a string for embedding in an event.
func renderToString(ctx context.Context, c templ.Component) string {
	var buf bytes.Buffer
	if err := c.Render(ctx, &buf); err != nil {
		return ""
	}
	return buf.String()
}

// writeEvent emits one SSE message. Payload newlines are re-emitted as separate
// data: lines, which the protocol requires and templ's output makes likely.
func writeEvent(w http.ResponseWriter, name, payload string) {
	fmt.Fprintf(w, "event: %s\n", name)
	for _, line := range strings.Split(payload, "\n") {
		fmt.Fprintf(w, "data: %s\n", line)
	}
	fmt.Fprint(w, "\n")
}
