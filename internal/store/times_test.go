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

	if err := st.RecordCheckFailure(ctx, "Alpha", "/x/", "broken", "attempt to index a nil value"); err != nil {
		t.Fatal(err)
	}
	if err := st.RecordCheckFailure(ctx, "Beta", "/x/", "down", "502"); err != nil {
		t.Fatal(err)
	}
	if s := get(1); s.CheckProblem != "broken" || s.CheckError == "" || !s.CheckFailedAt.Valid {
		t.Errorf("series 1 = %q %q %v, want its failure noted by key", s.CheckProblem, s.CheckError, s.CheckFailedAt)
	}
	if s := get(2); s.CheckProblem != "down" {
		t.Errorf("series 2 problem = %q, want its failure noted by label", s.CheckProblem)
	}
	if s := get(3); s.CheckError != "" {
		t.Errorf("series 3 = %q; another address on the same site must be left alone", s.CheckError)
	}

	// An unknown series is not an error: there is nothing to mark.
	if err := st.RecordCheckFailure(ctx, "Alpha", "/nowhere/", "down", "502"); err != nil {
		t.Errorf("unknown series: %v", err)
	}

	s := get(1)
	if _, err := st.UpsertSeries(ctx, s); err != nil {
		t.Fatal(err)
	}
	if s := get(1); s.CheckProblem != "" || s.CheckError != "" || s.CheckFailedAt.Valid {
		t.Errorf("after a working check: %q %q %v, want cleared", s.CheckProblem, s.CheckError, s.CheckFailedAt)
	}
}
