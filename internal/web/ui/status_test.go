package ui

import (
	"bytes"
	"context"
	"testing"

	"github.com/dyskette/atsume/internal/store"
)

// TestQueueStatus covers the footer: idle, busy with combined progress and
// Pause all, and paused with Resume.
func TestQueueStatus(t *testing.T) {
	render := func(v QueueView) string {
		var buf bytes.Buffer
		if err := QueueStatus(v).Render(context.Background(), &buf); err != nil {
			t.Fatal(err)
		}
		return buf.String()
	}

	idle := render(QueueView{})
	mustContain(t, idle, "Nothing in progress")
	mustNotContain(t, idle, "Pause all", "<progress")

	busy := render(QueueView{QueueStatus: store.QueueStatus{Downloading: 2, Queued: 1}, Done: 45, Total: 61})
	mustContain(t, busy, "Downloading · 2 active, 1 queued", `max="61"`, "45 / 61 pages", `hx-post="/queue/pause"`)

	paused := render(QueueView{QueueStatus: store.QueueStatus{Downloading: 1, Queued: 5}, Paused: true, Done: 3, Total: 20})
	mustContain(t, paused, "Paused · 1 finishing, 5 queued", `hx-post="/queue/resume"`, "Resume")
	mustNotContain(t, paused, "Pause all")
}
