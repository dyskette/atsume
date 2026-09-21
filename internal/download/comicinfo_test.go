package download

import (
	"archive/zip"
	"encoding/xml"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestComicInfoCarriesWhatAFilenameCannot is the reason this exists.
//
// "1/2 Prince" cannot be a directory name, so the library server reading the
// folder calls the work "12 Prince" — a different title. The metadata states
// what it really is.
func TestComicInfoCarriesWhatAFilenameCannot(t *testing.T) {
	ch := ParseChapter("1/2 Prince", "Vol.01 Ch.001 - A Prince Is Born")
	ch.URL = "https://fanfox.net/manga/1_2_prince/v01/c001/1.html"

	// The file name has to lose the slash; there is no choice about that.
	if got := ch.Filename(); !strings.HasPrefix(got, "12 Prince") {
		t.Fatalf("filename = %q; the premise of this test is that it is sanitised", got)
	}

	info := BuildComicInfo(SeriesMeta{
		Title:    "1/2 Prince",
		Summary:  "A summary.",
		Authors:  "Yu Wo",
		Artists:  "Choi Hong Chong",
		Genres:   "Action, Comedy",
		Status:   "completed",
		URL:      "https://fanfox.net/manga/1_2_prince/",
		Site:     "FanFox",
		Chapters: 81,
	}, ch, 73)

	if info.Series != "1/2 Prince" {
		t.Errorf("Series = %q, want the real title including the slash", info.Series)
	}
	if info.Title != "Vol.01 Ch.001 - A Prince Is Born" {
		t.Errorf("Title = %q", info.Title)
	}
	// The file name pads to three digits so a listing sorts; nobody calls
	// this chapter 001.
	if info.Number != "1" {
		t.Errorf("Number = %q, want it unpadded", info.Number)
	}
	if info.Count != 81 {
		t.Errorf("Count = %d, want the total for a finished work", info.Count)
	}
	if info.Writer != "Yu Wo" || info.Penciller != "Choi Hong Chong" {
		t.Errorf("credits = %q / %q", info.Writer, info.Penciller)
	}
	if info.PageCount != 73 {
		t.Errorf("PageCount = %d", info.PageCount)
	}
	if info.Web == "" || !strings.Contains(info.Notes, "FanFox") {
		t.Errorf("provenance missing: web=%q notes=%q", info.Web, info.Notes)
	}
}

// TestComicInfoOmitsVolume pins a decision that would otherwise be silently
// wrong.
//
// Komga appends a volume above 1 to the series title and aggregates that
// across a folder, so a series whose chapters span volumes would end up
// titled after whichever book was read last. The volume stays in the file
// name, which Komga already parses.
func TestComicInfoOmitsVolume(t *testing.T) {
	ch := ParseChapter("Some Series", "Vol.04 Ch.021 - Later")
	if ch.Volume == "" {
		t.Fatal("the premise is that a volume was parsed")
	}
	body, err := BuildComicInfo(SeriesMeta{Title: "Some Series"}, ch, 20).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "<Volume>") {
		t.Errorf("a volume would rename the series in Komga:\n%s", body)
	}
	// It is still in the file name, where it belongs.
	if !strings.Contains(ch.Filename(), "(v04)") {
		t.Errorf("filename = %q, want the volume kept there", ch.Filename())
	}
}

// TestComicInfoOngoingHasNoCount covers the difference between a work that is
// finished and one that is not.
func TestComicInfoOngoingHasNoCount(t *testing.T) {
	ch := ParseChapter("Ongoing Thing", "Chapter 5")
	info := BuildComicInfo(SeriesMeta{
		Title: "Ongoing Thing", Status: "ongoing", Chapters: 5,
	}, ch, 10)
	if info.Count != 0 {
		// Komga shows this as the book count; a running total says the
		// library is incomplete when it is merely current.
		t.Errorf("Count = %d, want none while the work is ongoing", info.Count)
	}
}

// TestComicInfoNumberFormats covers what sites actually put in a chapter
// number.
func TestComicInfoNumberFormats(t *testing.T) {
	cases := map[string]string{
		"001":   "1",
		"010.5": "10.5",
		"1":     "1",
		"":      "",
		"Extra": "Extra",
	}
	for in, want := range cases {
		if got := comicInfoNumber(in); got != want {
			t.Errorf("comicInfoNumber(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWriteCBZIncludesMetadata covers the file landing where a reader looks.
func TestWriteCBZIncludesMetadata(t *testing.T) {
	ch := ParseChapter("1/2 Prince", "Chapter 1")
	path := ch.Path(t.TempDir())
	pages := []Page{{Data: []byte("image"), Ext: ".jpg"}}
	info := BuildComicInfo(SeriesMeta{Title: "1/2 Prince", Site: "FanFox"}, ch, len(pages))

	if err := WriteCBZ(path, pages, info); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("archive holds %d entries, want the metadata and the page", len(zr.File))
	}
	// At the archive root, and first: a reader should not have to walk past
	// a hundred images to find it.
	if zr.File[0].Name != "ComicInfo.xml" {
		t.Fatalf("first entry = %q", zr.File[0].Name)
	}

	rc, err := zr.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()

	// It has to parse, or the library server ignores it and nothing says why.
	var parsed ComicInfo
	if err := xml.Unmarshal(body, &parsed); err != nil {
		t.Fatalf("the metadata does not parse: %v\n%s", err, body)
	}
	if parsed.Series != "1/2 Prince" {
		t.Errorf("Series round-tripped as %q", parsed.Series)
	}
	if !strings.HasPrefix(string(body), "<?xml") {
		t.Error("want an XML declaration")
	}
}

// TestWriteCBZWithoutMetadata covers the archive still being written when
// there is nothing to say about it.
func TestWriteCBZWithoutMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bare.cbz")
	if err := WriteCBZ(path, []Page{{Data: []byte("x"), Ext: ".jpg"}}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 1 || zr.File[0].Name != "0001.jpg" {
		t.Errorf("archive holds %d entries", len(zr.File))
	}
}
