package download

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseChapter(t *testing.T) {
	cases := []struct{ series, name, wantNum, wantVol string }{
		{"Solo Leveling", "Chapter 1", "001", ""},
		{"Solo Leveling", "Chapter 12", "012", ""},
		{"Solo Leveling", "Ch.125", "125", ""},
		{"Solo Leveling", "Chapter 12.5", "012.5", ""},
		{"Solo Leveling", "Vol.2 Chapter 15", "015", "02"},
		{"Solo Leveling", "Volume 10 Ch. 3", "003", "10"},
		{"Solo Leveling", "7 - The Beginning", "007", ""},
		{"Solo Leveling", "Oneshot", "000", ""},

		// A number in the series title is not the chapter's.
		{"Kaiju No. 8", "Kaiju No. 8 Chapter 100", "100", ""},
		{"Kaiju No. 8", "kaiju no. 8 100", "100", ""},
		{"Solo Leveling 2", "Solo Leveling 2 - Chapter 5", "005", ""},

		// A marked number wins over the first one.
		{"Tower of God", "Season 2 Chapter 10", "010", "02"},
		{"Tower of God", "[Season 1] Ep. 0", "000", "01"},
		{"Tower of God", "[Season 3] Ep. 150", "150", "03"},
		{"Solo Leveling", "Episode 7: The Test", "007", ""},
		{"Solo Leveling", "2023 Special #45", "045", ""},
		{"El escuadrón V", "Capítulo 3", "003", ""},
		{"El escuadrón V", "Capitulo 1", "001", ""},
		{"Спаривание", "Глава 5", "005", ""},
		{"進撃の巨人", "第12話", "012", ""},
		{"Solo Leveling", "Chapitre 9", "009", ""},

		// An explicit volume beats a season, and "ep" inside a word is no marker.
		{"Solo Leveling", "Season 2 Vol.4 Chapter 30", "030", "04"},
		{"Solo Leveling", "Epilogue 2", "002", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseChapter(c.series, c.name)
			if got.Number != c.wantNum {
				t.Errorf("Number = %q, want %q", got.Number, c.wantNum)
			}
			if got.Volume != c.wantVol {
				t.Errorf("Volume = %q, want %q", got.Volume, c.wantVol)
			}
		})
	}
}

func TestFilename(t *testing.T) {
	cases := []struct{ series, name, want string }{
		{"Solo Leveling", "Chapter 1", "Solo Leveling - c001.cbz"},
		{"Solo Leveling", "Vol.2 Chapter 15", "Solo Leveling - c015 (v02).cbz"},
		{`Re:Zero / Season 2`, "Chapter 3", "ReZero Season 2 - c003.cbz"},
	}
	for _, c := range cases {
		if got := ParseChapter(c.series, c.name).Filename(); got != c.want {
			t.Errorf("Filename(%q, %q) = %q, want %q", c.series, c.name, got, c.want)
		}
	}
}

// TestSanitize covers the names that break an SMB-shared library directory.
func TestCandidates(t *testing.T) {
	root := "/library"
	ch := ParseChapter("Solo Leveling", "Oneshot: The Hunter's Day")
	got := ch.Candidates(root, "1:/oneshot")
	want := []string{
		"/library/Solo Leveling/Solo Leveling - c000.cbz",
		"/library/Solo Leveling/Solo Leveling - c000 - Oneshot The Hunter's Day.cbz",
	}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("got %q", got)
	}
	if !strings.HasPrefix(got[2], strings.TrimSuffix(want[1], ".cbz")+" [") || !strings.HasSuffix(got[2], "].cbz") {
		t.Errorf("last candidate %q should add a hash to the second", got[2])
	}

	// The hash follows the key, so two chapters never share the last name.
	if other := ch.Candidates(root, "2:/oneshot"); other[2] == got[2] {
		t.Errorf("different keys gave the same last candidate %q", got[2])
	}
	// And the same chapter always asks for the same names.
	if again := ch.Candidates(root, "1:/oneshot"); again[2] != got[2] {
		t.Errorf("candidates changed between calls: %q, %q", got[2], again[2])
	}

	// A volume stays in the stem, a long name is cut on a rune boundary, and a
	// nameless chapter skips straight to the hash.
	long := ParseChapter("Tower of God", "[Season 2] Ep. 1 "+strings.Repeat("長", 100))
	c := long.Candidates(root, "k")
	if !strings.Contains(c[1], "Tower of God - c001 (v02) - ") || !utf8.ValidString(c[1]) ||
		len(filepath.Base(c[2])) > maxName {
		t.Errorf("long name: %q", c[1])
	}
	if nameless := (Chapter{Series: "S", Number: "000"}).Candidates(root, "k"); len(nameless) != 2 {
		t.Errorf("nameless chapter: %q", nameless)
	}
}

func TestSanitize(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Normal Title", "Normal Title"},
		{`bad<>:"/\|?*chars`, "badchars"},
		{"trailing dot.", "trailing dot"},
		{"  collapsed   spaces  ", "collapsed spaces"},
		{`///`, "Unknown"},
	}
	for _, c := range cases {
		if got := Sanitize(c.in); got != c.want {
			t.Errorf("Sanitize(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestSanitizeCutsOnCharacters covers a title longer than maxTitle: it must
// shrink to fit without splitting a character, or the name on disk would not
// be valid UTF-8.
func TestSanitizeCutsOnCharacters(t *testing.T) {
	for _, in := range []string{
		strings.Repeat("進撃の巨人", 30),       // three bytes a character
		"a" + strings.Repeat("進撃の巨人", 30), // and shifted off the boundary
		strings.Repeat("😀", 60),           // four bytes a character
		strings.Repeat("x", 199) + " yz",  // a cut landing on a space
		strings.Repeat("x", 199) + ".yz",  // or on a dot, which Windows rejects last
	} {
		got := Sanitize(in)
		if !utf8.ValidString(got) || len(got) > maxTitle || strings.HasSuffix(got, " ") || strings.HasSuffix(got, ".") {
			t.Errorf("Sanitize(%.20q…) = %q (%d bytes)", in, got, len(got))
		}
		if !strings.HasPrefix(in, got) {
			t.Errorf("Sanitize(%.20q…) should keep the start of the title, got %q", in, got)
		}
	}
}

// TestLongNamesFitTheFilesystem writes the longest names a chapter can ask
// for, with a long CJK series title and chapter name, to show every
// candidate stays within what the filesystem accepts.
func TestLongNamesFitTheFilesystem(t *testing.T) {
	root := t.TempDir()
	ch := ParseChapter(strings.Repeat("進撃の巨人", 40), "[Season 12] Ep. 345.5 "+strings.Repeat("長い話", 60))
	for _, path := range ch.Candidates(root, "key") {
		name := filepath.Base(path)
		if len(name) > maxName || !utf8.ValidString(name) {
			t.Errorf("%d bytes, valid %v: %q", len(name), utf8.ValidString(name), name)
		}
		if err := WriteCBZ(path, []Page{{Data: []byte("x"), Ext: ".jpg"}}, nil); err != nil {
			t.Errorf("writing %q: %v", name, err)
		}
	}
}

func TestWriteCBZ(t *testing.T) {
	dir := t.TempDir()
	ch := ParseChapter("Solo Leveling", "Chapter 1")
	path := ch.Path(dir)

	pages := []Page{
		{Data: []byte("first"), Ext: ".jpg"},
		{Data: []byte("second"), Ext: ".png"},
	}
	if err := WriteCBZ(path, pages, nil); err != nil {
		t.Fatal(err)
	}
	if got := filepath.Base(path); got != "Solo Leveling - c001.cbz" {
		t.Errorf("path = %q", got)
	}

	// The archive has to be readable by whatever scans the library, which is
	// rarely the process that wrote it.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o644 {
		t.Errorf("mode = %o, want 644", perm)
	}

	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	if len(zr.File) != 2 {
		t.Fatalf("got %d entries, want 2", len(zr.File))
	}
	// Names must sort lexically into reading order.
	if zr.File[0].Name != "0001.jpg" || zr.File[1].Name != "0002.png" {
		t.Errorf("entry names = %q, %q", zr.File[0].Name, zr.File[1].Name)
	}
}

// TestWriteCBZEmpty guards the case that would otherwise publish an empty
// archive into the library for Komga to index.
func TestWriteCBZEmpty(t *testing.T) {
	if err := WriteCBZ(filepath.Join(t.TempDir(), "x.cbz"), nil, nil); err == nil {
		t.Fatal("expected an error for an empty page list")
	}
}
