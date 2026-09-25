package web

import (
	"context"
	"strings"
	"testing"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/jobs"
	"github.com/dyskette/atsume/internal/store"
)

// TestQueueEventsCarryTheFooter covers the footer's live update: a check
// finishing sends the footer's contents as a "queue" event.
func TestQueueEventsCarryTheFooter(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := &Server{App: &app.App{Store: st, Pool: &jobs.Pool{}}}

	evs := s.renderEvent(context.Background(), jobs.Event{
		Kind: "series-updated", SeriesID: 1, State: "done", Message: "Solo Leveling: 2 new chapters",
	})
	if len(evs) != 1 || evs[0].name != "queue" {
		t.Fatalf("events %v, want one queue event", evs)
	}
	if html := evs[0].html; !strings.Contains(html, "Nothing in progress") {
		t.Errorf("a queue event must carry the footer's status, got %q", html)
	}
}
