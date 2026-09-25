package web

import (
	"context"
	"strings"
	"testing"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/store"
)

// TestQueueEventsCarryTheFooter covers the footer's live update. The footer
// replaces its whole element with each "queue" event, so every one must carry
// that element; plain text would remove it and the footer would stop updating
// until the page was reloaded.
func TestQueueEventsCarryTheFooter(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{App: &app.App{Store: st}}

	name, html := s.renderEvent(context.Background(), jobs.Event{
		Kind: "series-updated", SeriesID: 1, State: "done", Message: "Solo Leveling: 2 new chapters",
	})
	if name != "queue" {
		t.Fatalf("event %q, want queue", name)
	}
	if !strings.Contains(html, `id="queue-status"`) {
		t.Errorf("a queue event must carry the footer element, got %q", html)
	}
}
