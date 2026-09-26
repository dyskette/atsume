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
	// New is a title the latest read found that the one before did not.
	New bool
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
	// Source is "site" when atsume read the site itself and "prebuilt" when
	// it downloaded a snapshot somebody else read. The two are not the same
	// claim and must not read alike.
	Source string
	// DataAt is how old the titles are, which for a snapshot is not when it
	// was downloaded.
	DataAt time.Time
	// Problem is what kind of failure ended the last read, "" when none.
	Problem string
	// NewTitles is how many titles the latest read found that the one
	// before did not.
	NewTitles int
	// Pages is how many directory pages the last complete read took, and
	// Steps how many the latest read got through.
	Pages, Steps int
	// Resume is where an unfinished read stopped; Resume.Dir is -1 when
	// there is nothing to carry on from.
	Resume ReadPos
	// OKAt is when the site last read all the way through.
	OKAt time.Time
	// RetryAt is when atsume tries a site that was down again, zero when it
	// will not, and Retries how many times it has.
	RetryAt time.Time
	Retries int
	// Failure is what the latest read ran into.
	Failure
}

// Failure is what a read ran into, for explaining it in plain words.
type Failure struct {
	// Status is the last HTTP status, 0 when the site did not answer.
	Status int
	// Challenged is an anti-bot page seen during the read.
	Challenged bool
	// Cause is why a site did not answer: "dns", "refused", "timeout" or "".
	Cause string
	// MovedTo is the address a request to the site was redirected to on
	// another host, "" when none was.
	MovedTo string
}

// ReadPos is a position in a site's directory: which section, which page.
type ReadPos struct{ Dir, Page int }

// NoPos is the position of a read with nothing to resume.
var NoPos = ReadPos{Dir: -1}

// FromSite reports whether atsume read this catalogue itself.
func (c SiteCatalogue) FromSite() bool { return c.Source != SourcePrebuilt }

// Age is how old the titles are, by the best date available.
func (c SiteCatalogue) Age() time.Time {
	if !c.DataAt.IsZero() {
		return c.DataAt
	}
	return c.BuiltAt
}

// Catalogue sources.
const (
	SourceSite     = "site"
	SourcePrebuilt = "prebuilt"
)

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
			complete = 0, note = '', read_seq = read_seq + 1,
			new_from = CASE WHEN EXISTS (SELECT 1 FROM site_title WHERE site = excluded.site)
			                THEN read_seq + 1 ELSE 0 END`, site); err != nil {
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
		INSERT INTO site_title (site, url, name, seq, seen_at, seen_read, first_read)
		VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP, ?, ?)
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
		if _, err := stmt.ExecContext(ctx, site, t.URL, CleanTitle(t.Name), t.Seq, read, read); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ReadOutcome is how a read of a site's catalogue ended.
type ReadOutcome struct {
	// Complete is a read that got to the end, which is the only kind that
	// may decide a title has gone.
	Complete bool
	Note     string
	Source   string
	// DataAt is how old the titles are.
	DataAt time.Time
	// Problem is the kind of failure that ended the read, "" when none.
	Problem string
	// Steps is how many directory pages the read got through.
	Steps int
	// Resume is where an unfinished read can carry on from, NoPos for none.
	Resume ReadPos
	// RetryAt is when atsume will try again by itself, and Retries how
	// many times it has so far.
	RetryAt time.Time
	Retries int
	Failure
}

// FinishSiteCatalogue records how the read ended.
//
// The timestamp is stamped here rather than at the start: "read 20 minutes
// ago" should mean the list is twenty minutes old, and a read of a large
// site takes minutes of that by itself.
func (s *Store) FinishSiteCatalogue(ctx context.Context, site string, read int64, o ReadOutcome) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Only a read that finished may decide a title has gone. One that failed
	// partway simply did not get there, and treating that as a deletion
	// would empty a catalogue because a site had a bad minute.
	if o.Complete {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM site_title WHERE site = ? AND seen_read <> ?`, site, read); err != nil {
			return err
		}
	}
	if o.Complete {
		o.Resume = NoPos
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE site_catalogue
		SET complete = ?, note = ?, source = ?, data_at = ?, problem = ?, built_at = CURRENT_TIMESTAMP,
		    steps = ?, resume_dir = ?, resume_page = ?, retry_at = ?, retries = ?,
		    status = ?, challenged = ?, cause = ?, moved_to = ?,
		    pages = CASE WHEN ? THEN ? ELSE pages END,
		    ok_at = CASE WHEN ? THEN CURRENT_TIMESTAMP ELSE ok_at END
		WHERE site = ?`,
		o.Complete, o.Note, o.Source, sqlTime(o.DataAt), o.Problem,
		o.Steps, o.Resume.Dir, o.Resume.Page, sqlTime(o.RetryAt), o.Retries,
		o.Status, o.Challenged, o.Cause, o.MovedTo,
		o.Complete && o.Source == SourceSite, o.Steps,
		o.Complete, site); err != nil {
		return err
	}
	return tx.Commit()
}

// ResumeSiteCatalogue picks an unfinished read back up: the read number its
// titles carry, where it stopped, how far it had got, and the titles it had
// already seen. ok is false when there is nothing to resume.
func (s *Store) ResumeSiteCatalogue(ctx context.Context, site string) (read int64, at ReadPos, steps int, seen []string, ok bool, err error) {
	err = s.DB.QueryRowContext(ctx, `
		SELECT read_seq, resume_dir, resume_page, steps FROM site_catalogue
		WHERE site = ? AND complete = 0 AND resume_dir >= 0`, site).
		Scan(&read, &at.Dir, &at.Page, &steps)
	if err == sql.ErrNoRows {
		return 0, NoPos, 0, nil, false, nil
	}
	if err != nil {
		return 0, NoPos, 0, nil, false, err
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT url FROM site_title WHERE site = ? AND seen_read = ?`, site, read)
	if err != nil {
		return 0, NoPos, 0, nil, false, err
	}
	defer rows.Close()
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return 0, NoPos, 0, nil, false, err
		}
		seen = append(seen, u)
	}
	if err := rows.Err(); err != nil {
		return 0, NoPos, 0, nil, false, err
	}
	_, err = s.DB.ExecContext(ctx,
		`UPDATE site_catalogue SET note = '', retry_at = NULL WHERE site = ?`, site)
	return read, at, steps, seen, err == nil, err
}

// OthersWorking reports whether any other site read through, or had a series
// checked, within the last hour: evidence that a failure is the site's and
// not the connection's.
func (s *Store) OthersWorking(ctx context.Context, site string) (bool, error) {
	var ok bool
	err := s.DB.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM site_catalogue
		               WHERE site <> ? AND julianday(ok_at) > julianday('now', '-1 hour'))
		    OR EXISTS (SELECT 1 FROM series
		               WHERE module_key <> ? AND julianday(checked_at) > julianday('now', '-1 hour'))`,
		site, site).Scan(&ok)
	return ok, err
}

// SiteCatalogueInfo reports what is stored for a site.
func (s *Store) SiteCatalogueInfo(ctx context.Context, site string) (SiteCatalogue, error) {
	out := SiteCatalogue{Site: site, Resume: NoPos}
	var built, data, okAt, retryAt sql.NullString
	err := s.DB.QueryRowContext(ctx, `
		SELECT built_at, complete, note, source, data_at, problem,
		       (SELECT COUNT(*) FROM site_title WHERE site = c.site),
		       (SELECT COUNT(*) FROM site_title WHERE site = c.site AND c.new_from > 0 AND first_read = c.new_from),
		       pages, steps, resume_dir, resume_page, ok_at, retry_at, retries,
		       status, challenged, cause, moved_to
		FROM site_catalogue c WHERE site = ?`, site).
		Scan(&built, &out.Complete, &out.Note, &out.Source, &data, &out.Problem, &out.Titles, &out.NewTitles,
			&out.Pages, &out.Steps, &out.Resume.Dir, &out.Resume.Page, &okAt, &retryAt, &out.Retries,
			&out.Status, &out.Challenged, &out.Cause, &out.MovedTo)
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
	if t := parseTimestamp(data); t.Valid {
		out.DataAt = t.Time
	}
	if t := parseTimestamp(okAt); t.Valid {
		out.OKAt = t.Time
	}
	if t := parseTimestamp(retryAt); t.Valid {
		out.RetryAt = t.Time
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
		`SELECT url, name, seq,
		        first_read = (SELECT new_from FROM site_catalogue WHERE site = site_title.site AND new_from > 0) AS new
		 FROM site_title WHERE `+where+`
		 ORDER BY new DESC, seq LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var out []SiteTitle
	for rows.Next() {
		var t SiteTitle
		var isNew sql.NullBool
		if err := rows.Scan(&t.URL, &t.Name, &t.Seq, &isNew); err != nil {
			return nil, 0, err
		}
		t.New = isNew.Bool
		out = append(out, t)
	}
	return out, total, rows.Err()
}

// escapeLike keeps a search for "50%" from matching everything.
func escapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// sqlTime formats t the way SQLite's CURRENT_TIMESTAMP does, in UTC, so the
// two compare as text; the zero time is NULL.
func sqlTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}
