package download

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
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
