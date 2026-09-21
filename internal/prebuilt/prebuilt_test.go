package prebuilt

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFetchSnapshot reads a published snapshot end to end, against a fixture
// built the way upstream builds them.
func TestFetchSnapshot(t *testing.T) {
	fixture := filepath.Join("testdata", "snapshot.7z")
	if _, err := os.Stat(fixture); err != nil {
		t.Skip("no snapshot fixture; run make testdata")
	}
	body, err := os.ReadFile(fixture)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/abc.7z" {
			http.NotFound(w, r)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	s := New(srv.URL+"/<id>.7z", srv.Client())
	snap, err := s.Fetch(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Titles) == 0 {
		t.Fatal("no titles read")
	}
	if snap.Newest.IsZero() {
		t.Error("a snapshot has to be able to date itself; a file's own date can be re-committed")
	}
	if snap.Bytes != int64(len(body)) {
		t.Errorf("bytes = %d, want %d", snap.Bytes, len(body))
	}
}

// TestMissingSnapshotIsNotAnError covers the common case: nearly half the
// sites have none published, and that is ordinary rather than a failure.
func TestMissingSnapshotIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	s := New(srv.URL+"/<id>.7z", srv.Client())
	if _, err := s.Fetch(context.Background(), "nothing"); err != ErrNoSnapshot {
		t.Errorf("err = %v, want ErrNoSnapshot", err)
	}
	// A site whose module declares no id has none by definition.
	if _, err := s.Fetch(context.Background(), ""); err != ErrNoSnapshot {
		t.Errorf("err = %v, want ErrNoSnapshot", err)
	}
}

// TestAddress covers both spellings of the location, since it is
// configurable and upstream's own option uses the placeholder form.
func TestAddress(t *testing.T) {
	if got := New("https://x/<id>.7z", nil).Address("abc"); got != "https://x/abc.7z" {
		t.Errorf("got %q", got)
	}
	if got := New("https://x/7z/", nil).Address("abc"); got != "https://x/7z/abc.7z" {
		t.Errorf("got %q", got)
	}
}

func TestFromJulianDay(t *testing.T) {
	// The day FanFox's published snapshot last gained an entry.
	if got := fromJulianDay(2460638).Format(time.DateOnly); got != "2024-11-23" {
		t.Errorf("got %q, want 2024-11-23", got)
	}
	if !fromJulianDay(0).IsZero() {
		t.Error("an absent day number is not a date")
	}
}
