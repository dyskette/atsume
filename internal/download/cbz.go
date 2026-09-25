// Package download turns a chapter's page list into a CBZ on disk.
package download

import (
	"archive/zip"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
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
	// URL is where the chapter was read from, recorded in the archive's
	// metadata so a file on disk can be traced back to its source.
	URL string
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

// Candidates lists the paths this chapter may be written to, preferred first.
//
// Two chapters can parse to the same number: seasons that restart their
// count, a oneshot and an extra with no number at all, a redrawn chapter
// published beside the original, or two series with the same title from
// different sites, which share a folder. The first candidate is Path; the
// next adds the chapter's own name, and the last adds a short hash of key,
// which the caller makes unique to the chapter. Every candidate is derived
// from the chapter alone, so a chapter asks for the same names each time and
// taking one never renames a file already written.
func (c Chapter) Candidates(root, key string) []string {
	dir := filepath.Join(root, Sanitize(c.Series))
	stem := strings.TrimSuffix(c.Filename(), ".cbz")
	sum := sha256.Sum256([]byte(key))
	hash := fmt.Sprintf(" [%x]", sum[:4])
	out := []string{filepath.Join(dir, stem+".cbz")}

	// The chapter's name gets whatever the file name has left under maxName
	// once the stem, the hash and the extension are counted, so even the
	// longest candidate fits. A name with no room left is skipped.
	label := ""
	if room := maxName - len(stem) - len(" - ") - len(hash) - len(".cbz"); room > 0 && strings.TrimSpace(c.Name) != "" {
		if name := truncateBytes(Sanitize(c.Name), room); name != "" {
			label = " - " + name
			out = append(out, filepath.Join(dir, stem+label+".cbz"))
		}
	}
	return append(out, filepath.Join(dir, stem+label+hash+".cbz"))
}

// truncateBytes shortens s to at most n bytes without splitting a
// character, so the result stays valid UTF-8.
func truncateBytes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return strings.TrimSpace(s[:n])
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
	return strings.TrimRight(truncateBytes(s, maxTitle), ". ")
}

// maxTitle caps a sanitised title, in bytes, leaving room within maxName for
// the rest of a chapter's file name.
const maxTitle = 200

// maxName is the longest file name, in bytes, that ext4, btrfs and most
// other filesystems accept.
const maxName = 255

// WriteCBZ writes pages into a CBZ at path, with the metadata a library
// server reads.
//
// It writes to a temporary file in the destination directory and renames on
// success, so an interrupted download never leaves a half-written archive for
// Komga's scanner to pick up.
//
// The metadata may be nil, which writes a bare archive. A failure to render
// it is not a failure to write the chapter: the pages are the point, and a
// library that can be corrected by hand beats a download that did not happen.
func WriteCBZ(path string, pages []Page, info *ComicInfo) (err error) {
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
	if info != nil {
		// First in the archive, because a reader looking for it should not
		// have to walk past a hundred images to find it. It is the one entry
		// worth compressing.
		if body, err := info.Marshal(); err != nil {
			slog.Warn("could not render metadata", "path", path, "err", err)
		} else {
			w, err := zw.CreateHeader(&zip.FileHeader{
				Name:   comicInfoName,
				Method: zip.Deflate,
			})
			if err != nil {
				return err
			}
			if _, err := w.Write(body); err != nil {
				return err
			}
		}
	}
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
	// CreateTemp makes the file owner-only. A library directory is normally
	// read by something else — a library server in another container, a share
	// — so an archive nobody else can open is not much use.
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
