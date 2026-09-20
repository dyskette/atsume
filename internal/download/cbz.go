// Package download turns a chapter's page list into a CBZ on disk.
package download

import (
	"archive/zip"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Page is one image destined for the archive, in reading order.
type Page struct {
	Data []byte
	Ext  string // including the leading dot, e.g. ".jpg"
}

// Chapter names the archive to write.
type Chapter struct {
	Series string
	Name   string
	Number string // normalised chapter number, e.g. "001"
	Volume string // optional
}

// Filename renders the Komga-compatible name for a chapter. Komga parses
// `Series - c001 (v01).cbz`, and getting this wrong yields a library that looks
// populated but sorts into nonsense, so the layout is fixed rather than
// configurable.
func (c Chapter) Filename() string {
	name := fmt.Sprintf("%s - c%s", Sanitize(c.Series), c.Number)
	if c.Volume != "" {
		name += fmt.Sprintf(" (v%s)", c.Volume)
	}
	return name + ".cbz"
}

// Path returns the full destination path under root.
func (c Chapter) Path(root string) string {
	return filepath.Join(root, Sanitize(c.Series), c.Filename())
}

// illegal matches the characters that are unsafe in a filename on either Linux
// or Windows, since the library directory is frequently shared over SMB.
var illegal = regexp.MustCompile(`[<>:"/\\|?*\x00-\x1f]`)

// Sanitize makes a title safe to use as a path component.
func Sanitize(s string) string {
	s = illegal.ReplaceAllString(s, "")
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	s = strings.TrimRight(s, ". ") // Windows rejects a trailing dot or space
	if s == "" {
		return "Unknown"
	}
	if len(s) > 200 {
		s = strings.TrimSpace(s[:200])
	}
	return s
}

// WriteCBZ writes pages into a CBZ at path.
//
// It writes to a temporary file in the destination directory and renames on
// success, so an interrupted download never leaves a half-written archive for
// Komga's scanner to pick up.
func WriteCBZ(path string, pages []Page) (err error) {
	if len(pages) == 0 {
		return fmt.Errorf("no pages to write to %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), ".atsume-*.cbz")
	if err != nil {
		return err
	}
	defer func() {
		tmp.Close()
		if err != nil {
			os.Remove(tmp.Name())
		}
	}()

	zw := zip.NewWriter(tmp)
	for i, p := range pages {
		ext := p.Ext
		if ext == "" {
			ext = ".jpg"
		}
		// Zero-padded names keep readers in sync with the site's page order.
		w, err := zw.CreateHeader(&zip.FileHeader{
			Name:   fmt.Sprintf("%04d%s", i+1, ext),
			Method: zip.Store, // images are already compressed
		})
		if err != nil {
			return err
		}
		if _, err := w.Write(p.Data); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
