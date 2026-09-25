package jobs

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/store"
)

func newQueue(t *testing.T) *Queue {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return NewQueue(st.DB)
}

func chapterIDs(t *testing.T, raws []json.RawMessage) []int {
	t.Helper()
	var out []int
	for _, raw := range raws {
		var p struct {
			ID int `json:"chapter_id"`
		}
		if err := json.Unmarshal(raw, &p); err != nil {
			t.Fatal(err)
		}
		out = append(out, p.ID)
	}
	return out
}

// TestPendingInOrderAndDelete covers what cancelling and queue positions rely
// on: jobs waiting to start listed in the order workers take them, and
// removing one of them without touching a job already running.
func TestPendingInOrderAndDelete(t *testing.T) {
	q := newQueue(t)
	ctx := context.Background()
	for id := 1; id <= 3; id++ {
		if _, err := q.Enqueue(ctx, KindDownloadChapter, map[string]int{"chapter_id": id}); err != nil {
			t.Fatal(err)
		}
	}
	order := func() []int {
		raws, err := q.PendingInOrder(ctx, KindDownloadChapter)
		if err != nil {
			t.Fatal(err)
		}
		return chapterIDs(t, raws)
	}
	if got := order(); len(got) != 3 || got[0] != 1 || got[2] != 3 {
		t.Fatalf("order %v, want [1 2 3]", got)
	}

	if n, err := q.DeletePending(ctx, KindDownloadChapter, "chapter_id", 2); err != nil || n != 1 {
		t.Fatalf("deleting a pending job: %d, %v", n, err)
	}
	if got := order(); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("after deleting 2: %v, want [1 3]", got)
	}

	// A job already claimed is not the queue's to delete.
	if _, err := q.Claim(ctx); err != nil {
		t.Fatal(err)
	}
	if n, err := q.DeletePending(ctx, KindDownloadChapter, "chapter_id", 1); err != nil || n != 0 {
		t.Errorf("deleting a running job: %d, %v; want it left alone", n, err)
	}
}

// TestPauseHoldsNewJobs covers pausing: queued work waits while the pool is
// paused, and starts at once when it resumes.
func TestPauseHoldsNewJobs(t *testing.T) {
	q := newQueue(t)
	var ran atomic.Int32
	p := &Pool{Queue: q, Workers: 2, Idle: time.Hour, Handlers: map[string]Handler{
		"test": func(context.Context, json.RawMessage) error { ran.Add(1); return nil },
	}}
	p.Pause()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go p.Run(ctx)

	for range 2 {
		if _, err := q.Enqueue(ctx, "test", map[string]int{}); err != nil {
			t.Fatal(err)
		}
		p.Notify()
	}
	time.Sleep(300 * time.Millisecond)
	if n := ran.Load(); n != 0 {
		t.Fatalf("%d jobs ran while paused", n)
	}
	if !p.Paused() {
		t.Error("Paused should report the pause")
	}

	// Idle is an hour, so only Resume's wake-up can start them this soon.
	p.Resume()
	deadline := time.Now().Add(3 * time.Second)
	for ran.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if n := ran.Load(); n != 2 {
		t.Errorf("%d of 2 jobs ran after resuming", n)
	}
}
