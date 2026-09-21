package store

import (
	"context"
	"database/sql"
	"strings"
	"time"
)

// SiteTitle is one entry of a site's catalogue as atsume read it.
type SiteTitle struct {
	URL  string
	Name string
	Seq  int
}

// SiteCatalogue is what is known about a site's stored title list.
//
// BuiltAt is the honest answer to "is this current" — the one question a
// cached list has to be able to answer, and the one FMD2 has never answered
// for its own prebuilt databases.
type SiteCatalogue struct {
	Site     string
	BuiltAt  time.Time
	Complete bool
	Note     string
	Titles   int
	// Exists distinguishes a site never read from one read and found empty.
	Exists bool
}

// BeginSiteCatalogue records that a read has started, returning the moment
// it did.
//
// What is already stored stays. Deleting first meant that pressing "read
// again" on a large site left the reader with no list for the minutes it
// took, and that a read which failed halfway left the site emptier than
// before they pressed it. Titles the site has dropped are removed at the
// end instead, and only by a read that got all the way through.
func (s *Store) BeginSiteCatalogue(ctx context.Context, site string) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO site_catalogue (site, built_at, complete, note, read_seq)
		VALUES (?, CURRENT_TIMESTAMP, 0, '', 1)
		ON CONFLICT (site) DO UPDATE SET
			complete = 0, note = '', read_seq = read_seq + 1`, site); err != nil {
		return 0, err
	}
	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT read_seq FROM site_catalogue WHERE site = ?`, site).Scan(&seq); err != nil {
		return 0, err
	}
	return seq, tx.Commit()
}

// AddSiteTitles appends what one read of the site turned up, stamping each
// with the read that saw it.
//
// Titles arrive while the read is still running, so a reader watching a slow
// site sees it fill rather than staring at a spinner. A URL already recorded
// is left alone: sites repeat entries across their own pages.
func (s *Store) AddSiteTitles(ctx context.Context, site string, read int64, titles []SiteTitle) error {
	if len(titles) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// A title already stored is refreshed rather than skipped: its name may
	// have changed, and seen_at is what marks it as still listed.
	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO site_title (site, url, name, seq, seen_at, seen_read)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT (site, url) DO UPDATE SET
			name = excluded.name, seq = excluded.seq,
			seen_at = CURRENT_TIMESTAMP, seen_read = excluded.seen_read`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range titles {
		if t.URL == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, site, t.URL, CleanTitle(t.Name), t.Seq, read); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// FinishSiteCatalogue records how the read ended.
//
// The timestamp is stamped here rather than at the start: "read 20 minutes
// ago" should mean the list is twenty minutes old, and a read of a large
// site takes minutes of that by itself.
func (s *Store) FinishSiteCatalogue(ctx context.Context, site string, complete bool, note string, read int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Only a read that finished may decide a title has gone. One that failed
	// partway simply did not get there, and treating that as a deletion
	// would empty a catalogue because a site had a bad minute.
	if complete {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM site_title WHERE site = ? AND seen_read <> ?`, site, read); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE site_catalogue
		SET complete = ?, note = ?, built_at = CURRENT_TIMESTAMP
		WHERE site = ?`, complete, note, site); err != nil {
		return err
	}
	return tx.Commit()
}

// SiteCatalogueInfo reports what is stored for a site.
func (s *Store) SiteCatalogueInfo(ctx context.Context, site string) (SiteCatalogue, error) {
	out := SiteCatalogue{Site: site}
	var built sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT built_at, complete, note,
		       (SELECT COUNT(*) FROM site_title WHERE site = ?)
		FROM site_catalogue WHERE site = ?`, site, site).
		Scan(&built, &out.Complete, &out.Note, &out.Titles)
	if err == sql.ErrNoRows {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	out.Exists = true
	if t := parseTimestamp(built); t.Valid {
		out.BuiltAt = t.Time
	}
	return out, nil
}

// SearchSiteTitles returns a slice of a site's catalogue, filtered.
//
// The search runs here rather than in the browser because a catalogue can
// hold thousands of titles, and shipping all of them so a script can hide
// most is how a page becomes unusable on the device most likely to be
// reading it.
func (s *Store) SearchSiteTitles(ctx context.Context, site, query string, offset, limit int) ([]SiteTitle, int, error) {
	where, args := `site = ?`, []any{site}
	if q := strings.TrimSpace(query); q != "" {
		where += ` AND name LIKE ? ESCAPE '\'`
		args = append(args, "%"+escapeLike(q)+"%")
	}

	var total int
	if err := s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM site_title WHERE `+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}

	rows, err := s.DB.QueryContext(ctx,
		`SELECT url, name, seq FROM site_title WHERE `+where+`
		 ORDER BY seq LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []SiteTitle
	for rows.Next() {
		var t SiteTitle
		if err := rows.Scan(&t.URL, &t.Name, &t.Seq); err != nil {
			return nil, 0, err
		}
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// escapeLike keeps a search for "50%" from matching everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}
