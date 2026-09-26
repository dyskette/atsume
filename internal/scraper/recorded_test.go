package scraper

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRecorded replays real traffic against real upstream modules.
//
// This is the only coverage that exercises markup atsume did not author. The
// recordings are a site's own content, so neither they nor the goldens derived
// from them are committed; the test skips without a local recording. Record
// a new case with atsume module record (see docs/MODULES.md), or re-record
// every case with:
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
	var c RecordedCase
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

	got, err := recordedResult(r, c)
	if err != nil {
		t.Fatal(err)
	}
	// A recorded run must actually extract something; a module that
	// silently returns nothing is the failure this whole suite exists
	// to catch.
	for _, p := range got.problems(c) {
		t.Error(p)
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
