package store

import (
	"context"
	"testing"
)

// TestDownloadedAt covers the download time: stamped when a chapter is done,
// cleared when it stops being, and the latest one reported per series.
func TestDownloadedAt(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `
		INSERT INTO series (id, module_id, module_name, url, title) VALUES (1, 'a', 'Alpha', '/one/', 'One');
		INSERT INTO chapters (id, series_id, url, name) VALUES (1, 1, '/one/1', 'c1'), (2, 1, '/one/2', 'c2')`); err != nil {
		t.Fatal(err)
	}

	progress := func() SeriesProgress {
		t.Helper()
		p, err := st.Progress(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return p[1]
	}
	chapter := func(id int64) Chapter {
		t.Helper()
		c, err := st.GetChapter(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return c
	}

	if p := progress(); p.LastDownload.Valid {
		t.Errorf("nothing downloaded, yet LastDownload = %v", p.LastDownload)
	}

	if err := st.SetChapterState(ctx, 1, ChapterDone, "/lib/c1.cbz", "", 3); err != nil {
		t.Fatal(err)
	}
	if c := chapter(1); !c.DownloadedAt.Valid {
		t.Error("a done chapter has no download time")
	}
	if p := progress(); !p.LastDownload.Valid {
		t.Error("a series with a done chapter has no LastDownload")
	}

	// A file that went missing puts the chapter back to pending.
	if err := st.SetChapterState(ctx, 1, ChapterPending, "", "", 0); err != nil {
		t.Fatal(err)
	}
	if c := chapter(1); c.DownloadedAt.Valid {
		t.Errorf("a pending chapter keeps download time %v", c.DownloadedAt)
	}
	if p := progress(); p.LastDownload.Valid {
		t.Errorf("no chapter on disk, yet LastDownload = %v", p.LastDownload)
	}
}

// TestCheckFailure covers noting a failed check on the series a refresh names,
// by key or, on rows from before keys, by label, and clearing it when a check
// works.
func TestCheckFailure(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `
		INSERT INTO series (id, module_id, module_name, module_key, url, title) VALUES
			(1, 'a', 'Alpha Label', 'Alpha', '/x/', 'One'),
			(2, 'b', 'Beta', '', '/x/', 'Two'),
			(3, 'a', 'Alpha Label', 'Alpha', '/y/', 'Three')`); err != nil {
		t.Fatal(err)
	}
	get := func(id int64) Series {
		t.Helper()
		s, err := st.GetSeries(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}

	if err := st.RecordCheckFailure(ctx, "Alpha", "/x/", "broken", "attempt to index a nil value", Failure{}); err != nil {
		t.Fatal(err)
	}
	blocked := Failure{Status: 403, Challenged: true, Cause: "timeout"}
	if err := st.RecordCheckFailure(ctx, "Beta", "/x/", "blocked", "403", blocked); err != nil {
		t.Fatal(err)
	}
	if s := get(1); s.CheckProblem != "broken" || s.CheckError == "" || !s.CheckFailedAt.Valid {
		t.Errorf("series 1 = %q %q %v, want its failure noted by key", s.CheckProblem, s.CheckError, s.CheckFailedAt)
	}
	if s := get(2); s.CheckProblem != "blocked" || s.CheckFailure != blocked {
		t.Errorf("series 2 = %q %+v, want its failure and its detail noted by label", s.CheckProblem, s.CheckFailure)
	}
	if s := get(3); s.CheckError != "" {
		t.Errorf("series 3 = %q; another address on the same site must be left alone", s.CheckError)
	}

	// An unknown series is not an error: there is nothing to mark.
	if err := st.RecordCheckFailure(ctx, "Alpha", "/nowhere/", "down", "502", Failure{}); err != nil {
		t.Errorf("unknown series: %v", err)
	}

	s := get(1)
	if _, err := st.UpsertSeries(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s := get(1); s.CheckProblem != "" || s.CheckError != "" || s.CheckFailedAt.Valid {
		t.Errorf("after a working check: %q %q %v, want cleared", s.CheckProblem, s.CheckError, s.CheckFailedAt)
	}
	s = get(2)
	if _, err := st.UpsertSeries(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s := get(2); s.CheckFailure != (Failure{}) {
		t.Errorf("after a working check the detail is %+v, want cleared", s.CheckFailure)
	}
	if s.CreatedAt.IsZero() {
		t.Error("a series has no time it entered the library")
	}
}

// TestProgressTally covers what the library row reads beyond the counts: the
// newest chapter on disk, and which of the chapters in flight have started
// and which are new.
func TestProgressTally(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.DB.ExecContext(ctx, `
		INSERT INTO series (id, module_id, module_name, url, title) VALUES (1, 'a', 'Alpha', '/one/', 'One'), (2, 'a', 'Alpha', '/two/', 'Two');
		INSERT INTO chapters (series_id, url, name, number, state, backlog) VALUES
			(1, '/one/9', 'c9', '9', 'done', 1),
			(1, '/one/10', 'c10', '10', 'done', 1),
			(1, '/one/2.001', 's2c1', '2.001', 'done', 1),
			(1, '/one/x', 'extra', '', 'done', 1),
			(1, '/one/11', 'c11', '11', 'downloading', 0),
			(1, '/one/12', 'c12', '12', 'queued', 0),
			(1, '/one/1', 'c1', '1', 'queued', 1),
			(2, '/two/1', 'c1', '', 'done', 1)`); err != nil {
		t.Fatal(err)
	}
	all, err := st.Progress(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// Ten, not nine: numbers compare as numbers, not as text.
	if p := all[1]; p.Latest != "10" || p.Active != 3 || p.Downloading != 1 || p.NewActive != 2 {
		t.Errorf("tally = %+v, want latest 10, 3 active of which 1 downloading and 2 new", p)
	}
	if p := all[2]; p.Latest != "" || p.Done != 1 {
		t.Errorf("a done chapter without a number: %+v, want no latest", p)
	}
}
