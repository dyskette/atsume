// Package prebuilt reads the catalogue snapshots FMD2 publishes, so a site's
// title list can arrive in seconds instead of minutes of crawling.
//
// The snapshots live in a separate repository from the modules and are
// maintained by hand and by pull request, which means their freshness varies
// enormously: some are days old, some are years. Nothing here hides that —
// each snapshot dates itself from its own rows, and the interface says so.
package prebuilt

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
	_ "modernc.org/sqlite" // the snapshot is a SQLite file
)

// DefaultURL is where FMD2 publishes its snapshots. The placeholder is the
// module id, which is what a snapshot is named after.
const DefaultURL = "https://raw.githubusercontent.com/dazedcat19/FMD2-DB/master/7z/<id>.7z"

// maxSnapshot bounds a download. The largest published snapshot is under
// 50 MB; anything far past that is a sign the address is wrong, not that a
// site is enormous.
const maxSnapshot = 128 << 20

// Title is one entry of a snapshot.
type Title struct {
	URL  string
	Name string
}

// Snapshot is a site's catalogue as somebody else read it.
type Snapshot struct {
	Titles []Title
	// Newest is the date of the most recent entry, taken from the rows
	// themselves rather than from the file. It is the honest answer to how
	// old the list is: a file can be re-committed without its contents
	// changing.
	Newest time.Time
	// Bytes is what the download cost.
	Bytes int64
}

// ErrNoSnapshot means the site has none published, which is true of nearly
// half of them.
var ErrNoSnapshot = fmt.Errorf("no published snapshot for this site")

// Source fetches published snapshots.
type Source struct {
	URL    string
	Client *http.Client
}

// New builds a Source. An empty url uses the published location.
func New(url string, client *http.Client) *Source {
	if url == "" {
		url = DefaultURL
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	return &Source{URL: url, Client: client}
}

// Address is where a module id's snapshot lives.
func (s *Source) Address(id string) string {
	if strings.Contains(s.URL, "<id>") {
		return strings.ReplaceAll(s.URL, "<id>", id)
	}
	return strings.TrimSuffix(s.URL, "/") + "/" + id + ".7z"
}

// Fetch downloads and reads a site's snapshot.
//
// The archive holds one SQLite file, which has to be on disk for the driver
// to open it, so it is unpacked into a temporary directory and removed.
func (s *Source) Fetch(ctx context.Context, id string) (*Snapshot, error) {
	if id == "" {
		return nil, ErrNoSnapshot
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.Address(id), nil)
	if err != nil {
		return nil, err
	}
	res, err := s.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return nil, ErrNoSnapshot
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("snapshot for %s: %s", id, res.Status)
	}

	dir, err := os.MkdirTemp("", "atsume-snapshot-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	archive := filepath.Join(dir, "snapshot.7z")
	f, err := os.Create(archive)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(res.Body, maxSnapshot))
	f.Close()
	if err != nil {
		return nil, err
	}

	db, err := unpack(archive, dir)
	if err != nil {
		return nil, err
	}
	snap, err := read(ctx, db)
	if err != nil {
		return nil, err
	}
	snap.Bytes = n
	return snap, nil
}

// unpack extracts the single database the archive holds.
func unpack(archive, dir string) (string, error) {
	r, err := sevenzip.OpenReader(archive)
	if err != nil {
		return "", fmt.Errorf("unpack: %w", err)
	}
	defer r.Close()

	for _, f := range r.File {
		if !strings.HasSuffix(strings.ToLower(f.Name), ".db") {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		out := filepath.Join(dir, "snapshot.db")
		w, err := os.Create(out)
		if err != nil {
			rc.Close()
			return "", err
		}
		_, err = io.Copy(w, io.LimitReader(rc, maxSnapshot))
		rc.Close()
		w.Close()
		if err != nil {
			return "", err
		}
		return out, nil
	}
	return "", fmt.Errorf("unpack: the archive holds no database")
}

// read pulls the titles out of a snapshot.
//
// The table is FMD2's own, so this is a second contract with upstream
// alongside the Lua one: a schema change here is a break atsume has to
// notice rather than quietly read as an empty catalogue.
func read(ctx context.Context, path string) (*Snapshot, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.QueryContext(ctx, `SELECT link, title, jdn FROM masterlist`)
	if err != nil {
		return nil, fmt.Errorf("read snapshot: %w", err)
	}
	defer rows.Close()

	out := &Snapshot{}
	var newest int64
	for rows.Next() {
		var link string
		var title sql.NullString
		var jdn sql.NullInt64
		if err := rows.Scan(&link, &title, &jdn); err != nil {
			return nil, err
		}
		if link == "" {
			continue
		}
		out.Titles = append(out.Titles, Title{URL: link, Name: title.String})
		if jdn.Valid && jdn.Int64 > newest {
			newest = jdn.Int64
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out.Titles) == 0 {
		return nil, fmt.Errorf("read snapshot: it holds no titles")
	}
	out.Newest = fromJulianDay(newest)
	return out, nil
}

// fromJulianDay converts the day number FMD2 stores into a date.
func fromJulianDay(jdn int64) time.Time {
	if jdn <= 0 {
		return time.Time{}
	}
	// 2440588 is 1 January 1970 in Julian day numbers.
	return time.Unix((jdn-2440588)*86400, 0).UTC()
}
