package download

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestParseChapter(t *testing.T) {
	cases := []struct{ name, wantNum, wantVol string }{
		{"Chapter 1", "001", ""},
		{"Chapter 12", "012", ""},
		{"Ch.125", "125", ""},
		{"Chapter 12.5", "012.5", ""},
		{"Vol.2 Chapter 15", "015", "02"},
		{"Volume 10 Ch. 3", "003", "10"},
		{"7 - The Beginning", "007", ""},
		{"Oneshot", "000", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseChapter("Solo Leveling", c.name)
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
