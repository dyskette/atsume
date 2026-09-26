package scraper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// RecordedCase describes a module run against recorded traffic from a real
// site: the case.json of a directory under testdata/recorded.
type RecordedCase struct {
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

// goldenInfo is the extracted metadata a golden file pins.
type goldenInfo struct {
	Title        string   `json:"title"`
	AltTitles    string   `json:"alt_titles"`
	CoverLink    string   `json:"cover_link"`
	Authors      string   `json:"authors"`
	Artists      string   `json:"artists"`
	Genres       string   `json:"genres"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	ChapterLinks []string `json:"chapter_links"`
	ChapterNames []string `json:"chapter_names"`
}

// goldenResult is the whole output of running a module against its fixtures.
type goldenResult struct {
	Template  string      `json:"template"`
	Directory []Entry     `json:"directory,omitempty"`
	Info      *goldenInfo `json:"info"`
	Pages     []string    `json:"pages"`
}

// recordedResult runs a case's handlers and gathers what its golden pins.
func recordedResult(r *Runner, c RecordedCase) (goldenResult, error) {
	got := goldenResult{Template: c.Module}
	info, err := r.GetInfo(c.SeriesURL)
	if err != nil {
		return got, fmt.Errorf("GetInfo: %w", err)
	}
	got.Info = &goldenInfo{
		Title: info.Title, AltTitles: info.AltTitles, CoverLink: info.CoverLink,
		Authors: info.Authors, Artists: info.Artists, Genres: info.Genres,
		Status: info.Status, Summary: info.Summary,
		ChapterLinks: info.ChapterLinks.All(), ChapterNames: info.ChapterNames.All(),
	}
	if c.ChapterURL != "" {
		if got.Pages, err = r.GetPageNumber(c.ChapterURL); err != nil {
			return got, fmt.Errorf("GetPageNumber: %w", err)
		}
	}
	return got, nil
}

// problems says what makes a recorded result not worth pinning: a module
// that silently returns nothing is the failure recorded cases exist to catch.
func (g goldenResult) problems(c RecordedCase) []string {
	var out []string
	if g.Info.Title == "" {
		out = append(out, "title is empty")
	}
	switch {
	case c.ExpectNoChapters != "":
		// Pinning the absence: if chapters ever appear, the recorded
		// explanation has gone stale and needs revisiting.
		if len(g.Info.ChapterLinks) > 0 {
			out = append(out, fmt.Sprintf("expected no chapters (%s) but found %d",
				c.ExpectNoChapters, len(g.Info.ChapterLinks)))
		}
	case len(g.Info.ChapterLinks) == 0:
		out = append(out, "no chapters were extracted")
	}
	if c.ChapterURL != "" && len(g.Pages) == 0 {
		out = append(out, "no pages were extracted")
	}
	return out
}

// RecordSummary is what recording a case captured.
type RecordSummary struct {
	Title           string
	Chapters, Pages int
	Requests        int
}

// RecordCase runs a case's module against the live site, recording every
// response, and writes the case to dir: case.json, the recorded responses
// under cassette/ and the expected result in golden.json, which TestRecorded
// replays. A run that extracts nothing worth pinning writes nothing, and a
// case already in dir is replaced only by one that succeeded.
func RecordCase(ctx context.Context, luaDir, dir string, c RecordedCase) (RecordSummary, error) {
	var sum RecordSummary
	tmp := dir + ".recording"
	os.RemoveAll(tmp)
	defer os.RemoveAll(tmp)

	cassette, err := NewCassette(filepath.Join(tmp, "cassette"), true, nil)
	if err != nil {
		return sum, err
	}
	mod := filepath.Join(luaDir, "modules", c.Module+".lua")
	r, err := (&Host{LuaDir: luaDir, Transport: cassette}).Open(ctx, mod, "", "")
	if err != nil {
		return sum, err
	}
	defer r.Close()
	got, err := recordedResult(r, c)
	if err != nil {
		return sum, err
	}
	if p := got.problems(c); len(p) > 0 {
		return sum, errors.New("not recorded: " + strings.Join(p, "; ") + "; fix the module first")
	}

	caseJSON, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return sum, err
	}
	golden, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		return sum, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "case.json"), append(caseJSON, '\n'), 0o644); err != nil {
		return sum, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "golden.json"), append(golden, '\n'), 0o644); err != nil {
		return sum, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return sum, err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return sum, err
	}
	recorded, _ := os.ReadDir(filepath.Join(dir, "cassette"))
	return RecordSummary{
		Title: got.Info.Title, Chapters: len(got.Info.ChapterLinks), Pages: len(got.Pages),
		Requests: len(recorded),
	}, nil
}
