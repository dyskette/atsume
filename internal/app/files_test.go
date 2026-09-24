package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dyskette/atsume/internal/config"
	"github.com/dyskette/atsume/internal/download"
	"github.com/dyskette/atsume/internal/store"
)

// TestClaimFileKeepsChaptersApart covers the guarantee that no chapter's file
// is overwritten by another's: chapters sharing a number, downloading at the
// same time or one after the other, each get a file of their own.
func TestClaimFileKeepsChaptersApart(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	a := &App{Cfg: &config.Config{LibraryDir: t.TempDir()}, Store: st}

	id, err := st.UpsertSeries(ctx, store.Series{ModuleID: "m", ModuleName: "M", URL: "https://x/s", Title: "Solo Leveling"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReplaceChapters(ctx, id, []store.Chapter{
		{URL: "/oneshot", Name: "Oneshot", Number: "000"},
		{URL: "/extra", Name: "Extra", Number: "000"},
		{URL: "/extra-2", Name: "Extra", Number: "000"},
	}); err != nil {
		t.Fatal(err)
	}
	chs, err := st.ListChapters(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	byURL := map[string]store.Chapter{}
	for _, c := range chs {
		byURL[c.URL] = c
	}
	claim := func(c store.Chapter) (string, func()) {
		t.Helper()
		target := download.Chapter{Series: "Solo Leveling", Name: c.Name, Number: c.Number}
		path, release, err := a.claimFile(ctx, c, target)
		if err != nil {
			t.Fatal(err)
		}
		return path, release
	}
	base := func(p string) string { return filepath.Base(p) }

	// Downloading at once: the second and third see the claims in progress.
	oneshot, releaseOneshot := claim(byURL["/oneshot"])
	extra, releaseExtra := claim(byURL["/extra"])
	extra2, releaseExtra2 := claim(byURL["/extra-2"])
	if base(oneshot) != "Solo Leveling - c000.cbz" || base(extra) != "Solo Leveling - c000 - Extra.cbz" {
		t.Errorf("got %q and %q", base(oneshot), base(extra))
	}
	if extra2 == extra || extra2 == oneshot {
		t.Errorf("a third chapter with the same number and name got a taken file: %q", base(extra2))
	}

	// Once written and recorded, the store keeps the name taken.
	for c, p := range map[string]string{"/oneshot": oneshot, "/extra": extra, "/extra-2": extra2} {
		if err := st.SetChapterState(ctx, byURL[c].ID, store.ChapterDone, p, "", 1); err != nil {
			t.Fatal(err)
		}
	}
	releaseOneshot()
	releaseExtra()
	releaseExtra2()
	if again, release := claim(byURL["/extra"]); again != extra {
		t.Errorf("a re-download should rewrite its own file %q, got %q", base(extra), base(again))
	} else {
		release()
	}

	// A chapter with the same title from another series in the same folder
	// does not take over this series' file.
	other, err := st.UpsertSeries(ctx, store.Series{ModuleID: "n", ModuleName: "N", URL: "https://y/s", Title: "Solo Leveling"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.ReplaceChapters(ctx, other, []store.Chapter{{URL: "/oneshot", Name: "Oneshot", Number: "000"}}); err != nil {
		t.Fatal(err)
	}
	otherChs, err := st.ListChapters(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if p, release := claim(otherChs[0]); p == oneshot {
		t.Errorf("another series took %q", base(oneshot))
	} else {
		release()
	}
}
