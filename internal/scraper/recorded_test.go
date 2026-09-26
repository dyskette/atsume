package scraper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recordedCase describes a module run against recorded traffic from a real site.
type recordedCase struct {
	// Module is the upstream module name, loaded from the FMD2 checkout.
	Module string `json:"module"`
	// SeriesURL and ChapterURL are what the handlers are pointed at.
	SeriesURL  string `json:"series_url"`
	ChapterURL string `json:"chapter_url"`
	// Note explains why this site was chosen.
	Note string `json:"note,omitempty"`
	// ExpectNoChapters records a site that correctly lists none, with the
	// reason. Without it the harness would report a verified, explained state
	// as a failure, and the explanation would live nowhere.
	ExpectNoChapters string `json:"expect_no_chapters,omitempty"`
}

// TestRecorded replays real traffic against real upstream modules.
//
// This is the only coverage that exercises markup atsume did not author. The
// recordings are a site's own content, so neither they nor the goldens derived
// from them are committed; the test skips without a local recording. Capture
// one with:
//
//	ATSUME_RECORD=1 ATSUME_FMD2_DIR=... go test ./internal/scraper/ -run TestRecorded -update
func TestRecorded(t *testing.T) {
	root := filepath.Join("testdata", "recorded")
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Skip("no recorded cases")
	}

	luaRoot := luaDir(t)
	record := os.Getenv("ATSUME_RECORD") != ""

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) { runRecordedCase(t, root, luaRoot, e.Name(), record) })
	}
}

// runRecordedCase replays one recorded case and returns the runner it used,
// which TestCPULimitHeadroom inspects.
func runRecordedCase(t *testing.T, root, luaRoot, name string, record bool) *Runner {
	dir := filepath.Join(root, name)

	raw, err := os.ReadFile(filepath.Join(dir, "case.json"))
	if err != nil {
		t.Fatalf("%v", err)
	}
	var c recordedCase
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	cassetteDir := filepath.Join(dir, "cassette")
	if !record {
		if _, err := os.Stat(cassetteDir); err != nil {
			t.Skipf("no recording for %s; capture one with ATSUME_RECORD=1", c.Module)
		}
	}
	cassette, err := NewCassette(cassetteDir, record, nil)
	if err != nil {
		t.Fatal(err)
	}

	modFile := filepath.Join(luaRoot, "modules", c.Module+".lua")
	if _, err := os.Stat(modFile); err != nil {
		t.Skipf("module %s is not in this checkout", c.Module)
	}

	h := &Host{LuaDir: luaRoot, Transport: cassette}
	r, err := h.Open(context.Background(), modFile, "", "")
	// Skipped only when the checkout's module really is one Lua 5.5 rejects,
	// so a checkout where it is fixed — a fork, or upstream once it merges
	// the fix — runs the case.
	if isLua55Reject(err) {
		t.Skipf("%s assigns to a for loop variable, which Lua 5.5 rejects: %v", c.Module, err)
	}
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got := goldenResult{Template: c.Module}

	info, err := r.GetInfo(c.SeriesURL)
	if err != nil {
		t.Fatalf("GetInfo: %v", err)
	}
	got.Info = &goldenInfo{
		Title: info.Title, AltTitles: info.AltTitles, CoverLink: info.CoverLink,
		Authors: info.Authors, Artists: info.Artists, Genres: info.Genres,
		Status: info.Status, Summary: info.Summary,
		ChapterLinks: info.ChapterLinks.All(), ChapterNames: info.ChapterNames.All(),
	}

	if c.ChapterURL != "" {
		pages, err := r.GetPageNumber(c.ChapterURL)
		if err != nil {
			t.Fatalf("GetPageNumber: %v", err)
		}
		got.Pages = pages
	}

	// A recorded run must actually extract something; a module that
	// silently returns nothing is the failure this whole suite exists
	// to catch.
	if got.Info.Title == "" {
		t.Error("title is empty")
	}
	switch {
	case c.ExpectNoChapters != "":
		// Pinning the absence: if chapters ever appear, the recorded
		// explanation has gone stale and needs revisiting.
		if len(got.Info.ChapterLinks) > 0 {
			t.Errorf("expected no chapters (%s) but found %d",
				c.ExpectNoChapters, len(got.Info.ChapterLinks))
		}
	case len(got.Info.ChapterLinks) == 0:
		t.Error("no chapters were extracted")
	}
	if c.ChapterURL != "" && len(got.Pages) == 0 {
		t.Error("no pages were extracted")
	}

	goldenPath := filepath.Join(dir, "golden.json")
	out := mustJSON(t, got)
	if *updateGolden {
		if err := os.WriteFile(goldenPath, []byte(out+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d chapters, %d pages)",
			goldenPath, len(got.Info.ChapterLinks), len(got.Pages))
		return r
	}

	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Skipf("no golden yet; create one with -update")
	}
	if strings.TrimSpace(string(want)) != out {
		t.Errorf("output differs from %s\n--- want ---\n%s\n--- got ---\n%s",
			goldenPath, strings.TrimSpace(string(want)), out)
	}
	return r
}
