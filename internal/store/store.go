// Package store is the SQLite persistence layer.
//
// Queries are written by hand rather than generated. The schema is small enough
// that sqlc would add a build-time dependency without yet earning it; revisit
// that if the query set grows.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/store/migrations"
	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite" // pure-Go driver: keeps CGO_ENABLED=0
)

// Store wraps the database handle.
type Store struct{ DB *sql.DB }

// Open opens the database at dir/atsume.db and applies all migrations.
func Open(dir string) (*Store, error) {
	path := filepath.Join(dir, "atsume.db")
	// WAL lets the web reads proceed while a worker writes; busy_timeout covers
	// the brief moments SQLite still needs the write lock exclusively.
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite takes one writer at a time; more connections only add contention.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		db.Close()
		return nil, err
	}
	if err := goose.Up(db, "."); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return &Store{DB: db}, nil
}

// Close releases the database.
func (s *Store) Close() error { return s.DB.Close() }

// Series is a tracked title.
type Series struct {
	ID       int64
	ModuleID string
	// ModuleKey is the module's file name, which is what the registry is
	// indexed by. ModuleName is the label it declares, which differs for a
	// quarter of the catalogue and is only ever displayed.
	ModuleKey  string
	ModuleName string
	URL        string
	Title      string
	CoverURL   string
	Authors    string
	Artists    string
	Genres     string
	Status     string
	Summary    string
	Subscribed bool
	CheckedAt  sql.NullTime
}

// Chapter is one downloadable chapter.
type Chapter struct {
	ID       int64
	SeriesID int64
	URL      string
	Name     string
	Number   string
	Volume   string
	State    string
	FilePath string
	Error    string
	Pages    int
	Position int
}

// Chapter states.
const (
	ChapterPending     = "pending"
	ChapterQueued      = "queued"
	ChapterDownloading = "downloading"
	ChapterDone        = "done"
	ChapterFailed      = "failed"
)

// CleanTitle collapses the whitespace a scraped title arrives with.
//
// A title cut from a page keeps that page's indentation, which is invisible
// in a flat list and wrong everywhere else: it sorts before every letter, and
// the library is grouped and sorted.
func CleanTitle(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// EnsureSeries records a series if it is not already known, returning its id.
//
// Following creates the row on the click rather than leaving it to the job
// that fetches the details. Without it there is a window where a series has
// been followed and exists nowhere in the interface, which is how the library
// came to need a manual refresh to show what had just been added.
//
// Existing rows are left alone: the listing knows only a title, and it must
// not overwrite what a completed check has already found.
func (s *Store) EnsureSeries(ctx context.Context, v Series) (int64, bool, error) {
	const insert = `
		INSERT INTO series (module_id, module_key, module_name, url, title, subscribed)
		VALUES (?, ?, ?, ?, ?, 1)
		ON CONFLICT (module_id, url) DO NOTHING
		RETURNING id`
	var id int64
	err := s.DB.QueryRowContext(ctx, insert,
		v.ModuleID, v.ModuleKey, v.ModuleName, v.URL, CleanTitle(v.Title)).Scan(&id)
	if err == nil {
		return id, true, nil
	}
	if err != sql.ErrNoRows {
		return 0, false, err
	}

	err = s.DB.QueryRowContext(ctx,
		`SELECT id FROM series WHERE module_id = ? AND url = ?`, v.ModuleID, v.URL).Scan(&id)
	return id, false, err
}

// DeleteSeries forgets a series and everything atsume recorded about it.
//
// It never touches the files on disk. Those belong to the library server
// reading the destination directory, and a program that reaches into a media
// library to delete is one you forgive once. Removing a series here means
// atsume stops tracking it; what has already been written stays written.
//
// Pending jobs for it go too. A queued refresh would otherwise run after the
// removal and recreate the series from the site, which is the one way a
// remove could appear not to have worked.
func (s *Store) DeleteSeries(ctx context.Context, id int64) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Download jobs name a chapter, refresh jobs name the address. Both are
	// matched against this series before its rows go.
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM jobs
		WHERE state = 'pending' AND (
			(kind = 'download_chapter' AND json_extract(payload, '$.chapter_id') IN (
				SELECT id FROM chapters WHERE series_id = ?))
			OR (kind = 'refresh_series' AND json_extract(payload, '$.url') = (
				SELECT url FROM series WHERE id = ?))
		)`, id, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM chapters WHERE series_id = ?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetSeriesModule repoints a series at the site it came from, by name.
//
// Both columns are written: the key is what everything looks the site up by,
// and the name is what the reader sees, and they are the same thing now that
// a site is identified by the name it declares.
func (s *Store) SetSeriesModule(ctx context.Context, id int64, site string) error {
	_, err := s.DB.ExecContext(ctx,
		`UPDATE series SET module_key = ?, module_name = ? WHERE id = ?`, site, site, id)
	return err
}

// UpsertSeries inserts or updates a series and returns its id.
func (s *Store) UpsertSeries(ctx context.Context, v Series) (int64, error) {
	const q = `
		INSERT INTO series (module_id, module_key, module_name, url, title, cover_url,
		                    authors, artists, genres, status, summary, subscribed, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (module_id, url) DO UPDATE SET
			module_key = excluded.module_key,
			-- A check that scraped nothing must not destroy what the listing
			-- already told us. An empty title leaves a row with nothing to
			-- click, and an empty cover leaves a hole where the art was.
			title = CASE WHEN excluded.title <> '' THEN excluded.title ELSE series.title END,
			cover_url = CASE WHEN excluded.cover_url <> '' THEN excluded.cover_url ELSE series.cover_url END,
			authors = excluded.authors, artists = excluded.artists,
			genres = excluded.genres, status = excluded.status,
			summary = excluded.summary, checked_at = excluded.checked_at
		RETURNING id`
	var id int64
	err := s.DB.QueryRowContext(ctx, q, v.ModuleID, v.ModuleKey, v.ModuleName, v.URL,
		CleanTitle(v.Title), v.CoverURL, v.Authors, v.Artists, v.Genres, v.Status, v.Summary,
		v.Subscribed, time.Now()).Scan(&id)
	return id, err
}

// ListSeries returns every tracked series, newest first.
func (s *Store) ListSeries(ctx context.Context) ([]Series, error) {
	const q = `
		SELECT id, module_id, module_key, module_name, url, title, cover_url, authors,
		       artists, genres, status, summary, subscribed, checked_at
		FROM series ORDER BY title`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var v Series
		if err := rows.Scan(&v.ID, &v.ModuleID, &v.ModuleKey, &v.ModuleName, &v.URL, &v.Title,
			&v.CoverURL, &v.Authors, &v.Artists, &v.Genres, &v.Status,
			&v.Summary, &v.Subscribed, &v.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// QueueStatus is the state of all work, across every series.
type QueueStatus struct {
	Downloading int
	Queued      int
	Failed      int
}

// Busy reports whether anything is in flight.
func (q QueueStatus) Busy() bool { return q.Downloading+q.Queued > 0 }

// Queue summarises outstanding work for the status line.
func (s *Store) Queue(ctx context.Context) (QueueStatus, error) {
	const q = `SELECT state, COUNT(*) FROM chapters
	           WHERE state IN (?, ?, ?) GROUP BY state`
	rows, err := s.DB.QueryContext(ctx, q, ChapterDownloading, ChapterQueued, ChapterFailed)
	if err != nil {
		return QueueStatus{}, err
	}
	defer rows.Close()

	var out QueueStatus
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return out, err
		}
		switch state {
		case ChapterDownloading:
			out.Downloading = n
		case ChapterQueued:
			out.Queued = n
		case ChapterFailed:
			out.Failed = n
		}
	}
	return out, rows.Err()
}

// SeriesByModule lists the series that came from one site.
//
// The settings page shows them because a setting is changed in order to fix a
// series, and the page otherwise gives no way back to what prompted the visit.
func (s *Store) SeriesByModule(ctx context.Context, moduleKey string) ([]Series, error) {
	all, err := s.ListSeries(ctx)
	if err != nil {
		return nil, err
	}
	var out []Series
	for _, v := range all {
		if v.Key() == moduleKey || v.ModuleName == moduleKey {
			out = append(out, v)
		}
	}
	return out, nil
}

// TrackedURLs returns the series already followed from a module, keyed by the
// URL the site lists them under.
//
// A directory listing uses it to mark what is already in the library; without
// it the same series can be added twice with no warning.
func (s *Store) TrackedURLs(ctx context.Context, moduleName string) (map[string]int64, error) {
	// Matches either form: rows written before the key and the label were told
	// apart hold the label in module_name.
	rows, err := s.DB.QueryContext(ctx,
		`SELECT url, id FROM series WHERE module_key = ? OR module_name = ?`,
		moduleName, moduleName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int64{}
	for rows.Next() {
		var url string
		var id int64
		if err := rows.Scan(&url, &id); err != nil {
			return nil, err
		}
		out[url] = id
	}
	return out, rows.Err()
}

// SeriesProgress is the chapter tally for one series.
type SeriesProgress struct {
	Total   int
	Done    int
	Waiting int // pending or previously failed
	Active  int // queued or downloading
	Failed  int

	// New is what arrived while the series was being followed and is not yet
	// on disk. It is the difference between "a chapter came out" and "there
	// is a back catalogue here you never asked for", which the library used
	// to state in identical words.
	New int
	// NewestArrival is when a chapter last turned up for this series,
	// downloaded or not.
	NewestArrival sql.NullTime
}

// Progress returns the tally for every series in one query.
//
// The library page needs it for every row, and asking per row would be a query
// per series on the page a reader looks at most.
func (s *Store) Progress(ctx context.Context) (map[int64]SeriesProgress, error) {
	const q = `
		SELECT series_id, state, backlog, COUNT(*), MAX(arrived_at)
		FROM chapters GROUP BY series_id, state, backlog`
	rows, err := s.DB.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[int64]SeriesProgress{}
	for rows.Next() {
		var id int64
		var state string
		var backlog bool
		var n int
		var newest sql.NullString
		if err := rows.Scan(&id, &state, &backlog, &n, &newest); err != nil {
			return nil, err
		}
		arrived := parseTimestamp(newest)
		p := out[id]
		p.Total += n
		if !backlog {
			if state != ChapterDone {
				p.New += n
			}
			if arrived.Valid && (!p.NewestArrival.Valid || arrived.Time.After(p.NewestArrival.Time)) {
				p.NewestArrival = arrived
			}
		}
		switch state {
		case ChapterDone:
			p.Done += n
		case ChapterQueued, ChapterDownloading:
			p.Active += n
		case ChapterFailed:
			p.Failed += n
			p.Waiting += n
		default:
			p.Waiting += n
		}
		out[id] = p
	}
	return out, rows.Err()
}

// parseTimestamp reads a SQLite timestamp that arrived as text.
//
// The driver converts a column to time.Time from its declared type, and an
// aggregate has none: MAX(arrived_at) comes back as the string CURRENT_TIMESTAMP
// wrote, which is UTC and without a zone. A missing or unreadable value is
// simply absent — a library row is not worth failing a page over.
func parseTimestamp(v sql.NullString) sql.NullTime {
	if !v.Valid || v.String == "" {
		return sql.NullTime{}
	}
	for _, layout := range []string{"2006-01-02 15:04:05", time.RFC3339Nano} {
		if t, err := time.Parse(layout, v.String); err == nil {
			return sql.NullTime{Time: t.UTC(), Valid: true}
		}
	}
	return sql.NullTime{}
}

// GetSeries reads one series by id.
func (s *Store) GetSeries(ctx context.Context, id int64) (Series, error) {
	const q = `
		SELECT id, module_id, module_key, module_name, url, title, cover_url, authors,
		       artists, genres, status, summary, subscribed, checked_at
		FROM series WHERE id = ?`
	var v Series
	err := s.DB.QueryRowContext(ctx, q, id).Scan(&v.ID, &v.ModuleID, &v.ModuleKey,
		&v.ModuleName, &v.URL, &v.Title, &v.CoverURL, &v.Authors, &v.Artists,
		&v.Genres, &v.Status, &v.Summary, &v.Subscribed, &v.CheckedAt)
	return v, err
}

// ReplaceChapters records a series' chapter list, returning the chapters that
// were not previously known and whether this was the first look at the series.
//
// Existing rows keep their state, so a refresh never re-downloads what is
// already on disk. The newly seen chapters are what a subscription check acts
// on, which is why they are reported rather than counted.
//
// The first look is decided here rather than by the caller because it is the
// same question as "which URLs do we already have", and answering it inside
// the transaction means a concurrent check cannot make both answers true.
// Chapters found on that first look are marked as backlog: they were already
// published when the series was followed, and the library must not offer them
// as news.
func (s *Store) ReplaceChapters(ctx context.Context, seriesID int64, chs []Chapter) ([]Chapter, bool, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()

	known, err := knownChapterURLs(ctx, tx, seriesID)
	if err != nil {
		return nil, false, err
	}

	initial := len(known) == 0

	const q = `
		INSERT INTO chapters (series_id, url, name, number, volume, position,
		                      arrived_at, backlog)
		VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
		ON CONFLICT (series_id, url) DO UPDATE SET
			name = excluded.name, number = excluded.number,
			volume = excluded.volume, position = excluded.position
		RETURNING id`
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return nil, false, err
	}
	defer stmt.Close()

	var added []Chapter
	for i, c := range chs {
		var id int64
		if err := stmt.QueryRowContext(ctx, seriesID, c.URL, c.Name, c.Number, c.Volume, i, initial).Scan(&id); err != nil {
			return nil, false, err
		}
		if !known[c.URL] {
			c.ID, c.SeriesID, c.Position, c.State = id, seriesID, i, ChapterPending
			added = append(added, c)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return added, initial, nil
}

func knownChapterURLs(ctx context.Context, tx *sql.Tx, seriesID int64) (map[string]bool, error) {
	rows, err := tx.QueryContext(ctx, `SELECT url FROM chapters WHERE series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	known := map[string]bool{}
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		known[u] = true
	}
	return known, rows.Err()
}

// SeriesDueForCheck returns subscribed series whose last check is older than
// interval, oldest first, capped at limit.
//
// Ordering by checked_at means a backlog drains fairly rather than starving
// whichever series happens to sort last.
func (s *Store) SeriesDueForCheck(ctx context.Context, interval time.Duration, limit int) ([]Series, error) {
	const q = `
		SELECT id, module_id, module_key, module_name, url, title, cover_url, authors,
		       artists, genres, status, summary, subscribed, checked_at
		FROM series
		WHERE subscribed = 1 AND (checked_at IS NULL OR checked_at < ?)
		ORDER BY checked_at IS NOT NULL, checked_at
		LIMIT ?`
	rows, err := s.DB.QueryContext(ctx, q, time.Now().Add(-interval), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Series
	for rows.Next() {
		var v Series
		if err := rows.Scan(&v.ID, &v.ModuleID, &v.ModuleKey, &v.ModuleName, &v.URL, &v.Title,
			&v.CoverURL, &v.Authors, &v.Artists, &v.Genres, &v.Status,
			&v.Summary, &v.Subscribed, &v.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// SetSubscribed turns automatic checking for a series on or off.
func (s *Store) SetSubscribed(ctx context.Context, id int64, on bool) error {
	_, err := s.DB.ExecContext(ctx, `UPDATE series SET subscribed = ? WHERE id = ?`, on, id)
	return err
}

// ListChapters returns a series' chapters in reading order.
func (s *Store) ListChapters(ctx context.Context, seriesID int64) ([]Chapter, error) {
	const q = `
		SELECT id, series_id, url, name, number, volume, state, file_path, error, pages, position
		FROM chapters WHERE series_id = ? ORDER BY position`
	rows, err := s.DB.QueryContext(ctx, q, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Chapter
	for rows.Next() {
		var c Chapter
		if err := rows.Scan(&c.ID, &c.SeriesID, &c.URL, &c.Name, &c.Number,
			&c.Volume, &c.State, &c.FilePath, &c.Error, &c.Pages, &c.Position); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetChapter reads one chapter by id.
func (s *Store) GetChapter(ctx context.Context, id int64) (Chapter, error) {
	const q = `
		SELECT id, series_id, url, name, number, volume, state, file_path, error, pages, position
		FROM chapters WHERE id = ?`
	var c Chapter
	err := s.DB.QueryRowContext(ctx, q, id).Scan(&c.ID, &c.SeriesID, &c.URL,
		&c.Name, &c.Number, &c.Volume, &c.State, &c.FilePath, &c.Error, &c.Pages, &c.Position)
	return c, err
}

// ChapterByFile returns the ID of the chapter recorded as written to path, or
// 0 when none is.
func (s *Store) ChapterByFile(ctx context.Context, path string) (int64, error) {
	var id int64
	err := s.DB.QueryRowContext(ctx, `SELECT id FROM chapters WHERE file_path = ? LIMIT 1`, path).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// SetChapterState records progress or the outcome of a download.
func (s *Store) SetChapterState(ctx context.Context, id int64, state, filePath, errMsg string, pages int) error {
	const q = `UPDATE chapters SET state = ?, file_path = ?, error = ?, pages = ? WHERE id = ?`
	_, err := s.DB.ExecContext(ctx, q, state, filePath, errMsg, pages, id)
	return err
}

// PendingChapters returns chapters not yet downloaded for a series.
func (s *Store) PendingChapters(ctx context.Context, seriesID int64) ([]Chapter, error) {
	all, err := s.ListChapters(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	var out []Chapter
	for _, c := range all {
		if c.State == ChapterPending || c.State == ChapterFailed {
			out = append(out, c)
		}
	}
	return out, nil
}

// ModuleOptions reads the operator's overrides for a module.
func (s *Store) ModuleOptions(ctx context.Context, moduleName string) (map[string]string, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT name, value FROM module_options WHERE module_name = ?`, moduleName)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var name, value string
		if err := rows.Scan(&name, &value); err != nil {
			return nil, err
		}
		out[name] = value
	}
	return out, rows.Err()
}

// SetModuleOption stores one override.
func (s *Store) SetModuleOption(ctx context.Context, moduleName, name, value string) error {
	const q = `INSERT INTO module_options (module_name, name, value) VALUES (?, ?, ?)
	           ON CONFLICT (module_name, name) DO UPDATE SET value = excluded.value`
	_, err := s.DB.ExecContext(ctx, q, moduleName, name, value)
	return err
}

// Credentials are a module's stored login.
type Credentials struct {
	ModuleName string
	Username   string
	Password   string
}

// SetCredentials stores a module login, encrypting the password.
func (s *Store) SetCredentials(ctx context.Context, sealer *Sealer, c Credentials) error {
	if c.Username == "" && c.Password == "" {
		_, err := s.DB.ExecContext(ctx,
			`DELETE FROM module_credentials WHERE module_name = ?`, c.ModuleName)
		return err
	}
	sealed, err := sealer.Seal(c.Password)
	if err != nil {
		return err
	}
	const q = `INSERT INTO module_credentials (module_name, username, password_enc, updated_at)
	           VALUES (?, ?, ?, CURRENT_TIMESTAMP)
	           ON CONFLICT (module_name) DO UPDATE SET
	               username = excluded.username,
	               password_enc = excluded.password_enc,
	               updated_at = CURRENT_TIMESTAMP`
	_, err = s.DB.ExecContext(ctx, q, c.ModuleName, c.Username, sealed)
	return err
}

// Credentials reads a module login, or nil when none is stored.
func (s *Store) Credentials(ctx context.Context, sealer *Sealer, moduleName string) (*Credentials, error) {
	var username string
	var sealed []byte
	err := s.DB.QueryRowContext(ctx,
		`SELECT username, password_enc FROM module_credentials WHERE module_name = ?`,
		moduleName).Scan(&username, &sealed)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	password, err := sealer.Open(sealed)
	if err != nil {
		return nil, err
	}
	return &Credentials{ModuleName: moduleName, Username: username, Password: password}, nil
}

// HasCredentials reports whether a login is stored, without decrypting it.
func (s *Store) HasCredentials(ctx context.Context, moduleName string) bool {
	var n int
	_ = s.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM module_credentials WHERE module_name = ?`, moduleName).Scan(&n)
	return n > 0
}

// Setting reads a stored setting, returning def when unset.
func (s *Store) Setting(ctx context.Context, key, def string) string {
	var v string
	if err := s.DB.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v); err != nil {
		return def
	}
	return v
}

// SetSetting writes a setting.
func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	const q = `INSERT INTO settings (key, value) VALUES (?, ?)
	           ON CONFLICT (key) DO UPDATE SET value = excluded.value`
	_, err := s.DB.ExecContext(ctx, q, key, value)
	return err
}

// Key is the identifier a module is looked up by.
//
// Rows written before the key and the declared label were told apart hold the
// label; falling back to it keeps those resolvable, since the resolver accepts
// either form.
func (s Series) Key() string {
	if s.ModuleKey != "" {
		return s.ModuleKey
	}
	return s.ModuleName
}
