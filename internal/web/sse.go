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
	"github.com/dyskette/atsume/internal/web/ui"
)

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
			name, html := s.renderEvent(r.Context(), e)
			if name == "" {
				continue
			}
			writeEvent(w, name, html)
			flusher.Flush()
		}
	}
}

// renderEvent turns a bus event into an SSE event name and its HTML payload.
func (s *Server) renderEvent(ctx context.Context, e jobs.Event) (string, string) {
	switch e.Kind {
	case "chapter-progress":
		ch, err := s.App.Store.GetChapter(ctx, e.ChapterID)
		if err != nil {
			return "", ""
		}
		return fmt.Sprintf("chapter-%d", e.ChapterID),
			renderToString(ctx, ui.ChapterProgressRow(ch, e.Done, e.Total))

	case "chapter-updated":
		ch, err := s.App.Store.GetChapter(ctx, e.ChapterID)
		if err != nil {
			return "", ""
		}
		return fmt.Sprintf("chapter-%d", e.ChapterID),
			renderToString(ctx, ui.ChapterRow(ch))

	case "series-updated":
		return "queue", e.Message
	}
	return "", ""
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
