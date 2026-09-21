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

// BeginSiteCatalogue starts a fresh read of a site, discarding what was
// there.
//
// The old list goes at the start rather than being merged: a title the site
// has dropped should disappear, and reconciling two lists would keep it
// forever.
func (s *Store) BeginSiteCatalogue(ctx context.Context, site string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `DELETE FROM site_title WHERE site = ?`, site); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO site_catalogue (site, built_at, complete, note)
		VALUES (?, CURRENT_TIMESTAMP, 0, '')
		ON CONFLICT (site) DO UPDATE SET
			built_at = CURRENT_TIMESTAMP, complete = 0, note = ''`, site); err != nil {
		return err
	}
	return tx.Commit()
}

// AddSiteTitles appends what one read of the site turned up.
//
// Titles arrive while the read is still running, so a reader watching a slow
// site sees it fill rather than staring at a spinner. A URL already recorded
// is left alone: sites repeat entries across their own pages.
func (s *Store) AddSiteTitles(ctx context.Context, site string, titles []SiteTitle) error {
	if len(titles) == 0 {
		return nil
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO site_title (site, url, name, seq) VALUES (?, ?, ?, ?)
		ON CONFLICT (site, url) DO NOTHING`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, t := range titles {
		if t.URL == "" {
			continue
		}
		if _, err := stmt.ExecContext(ctx, site, t.URL, CleanTitle(t.Name), t.Seq); err != nil {
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
func (s *Store) FinishSiteCatalogue(ctx context.Context, site string, complete bool, note string) error {
	_, err := s.DB.ExecContext(ctx, `
		UPDATE site_catalogue
		SET complete = ?, note = ?, built_at = CURRENT_TIMESTAMP
		WHERE site = ?`, complete, note, site)
	return err
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
