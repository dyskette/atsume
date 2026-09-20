// Package store is the SQLite persistence layer.
//
// Queries are written by hand rather than generated. The schema is small enough
// that sqlc would add a build-time dependency without yet earning it; revisit
// that if the query set grows.
package store

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
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
	ID         int64
	ModuleID   string
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

// UpsertSeries inserts or updates a series and returns its id.
func (s *Store) UpsertSeries(ctx context.Context, v Series) (int64, error) {
	const q = `
		INSERT INTO series (module_id, module_name, url, title, cover_url,
		                    authors, artists, genres, status, summary, subscribed, checked_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (module_id, url) DO UPDATE SET
			title = excluded.title, cover_url = excluded.cover_url,
			authors = excluded.authors, artists = excluded.artists,
			genres = excluded.genres, status = excluded.status,
			summary = excluded.summary, checked_at = excluded.checked_at
		RETURNING id`
	var id int64
	err := s.DB.QueryRowContext(ctx, q, v.ModuleID, v.ModuleName, v.URL, v.Title,
		v.CoverURL, v.Authors, v.Artists, v.Genres, v.Status, v.Summary,
		v.Subscribed, time.Now()).Scan(&id)
	return id, err
}

// ListSeries returns every tracked series, newest first.
func (s *Store) ListSeries(ctx context.Context) ([]Series, error) {
	const q = `
		SELECT id, module_id, module_name, url, title, cover_url, authors,
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
		if err := rows.Scan(&v.ID, &v.ModuleID, &v.ModuleName, &v.URL, &v.Title,
			&v.CoverURL, &v.Authors, &v.Artists, &v.Genres, &v.Status,
			&v.Summary, &v.Subscribed, &v.CheckedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// GetSeries reads one series by id.
func (s *Store) GetSeries(ctx context.Context, id int64) (Series, error) {
	const q = `
		SELECT id, module_id, module_name, url, title, cover_url, authors,
		       artists, genres, status, summary, subscribed, checked_at
		FROM series WHERE id = ?`
	var v Series
	err := s.DB.QueryRowContext(ctx, q, id).Scan(&v.ID, &v.ModuleID, &v.ModuleName,
		&v.URL, &v.Title, &v.CoverURL, &v.Authors, &v.Artists, &v.Genres,
		&v.Status, &v.Summary, &v.Subscribed, &v.CheckedAt)
	return v, err
}

// ReplaceChapters records a series' chapter list and returns the chapters that
// were not previously known.
//
// Existing rows keep their state, so a refresh never re-downloads what is
// already on disk. The newly seen chapters are what a subscription check acts
// on, which is why they are reported rather than counted.
func (s *Store) ReplaceChapters(ctx context.Context, seriesID int64, chs []Chapter) ([]Chapter, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	known, err := knownChapterURLs(ctx, tx, seriesID)
	if err != nil {
		return nil, err
	}

	const q = `
		INSERT INTO chapters (series_id, url, name, number, volume, position)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT (series_id, url) DO UPDATE SET
			name = excluded.name, number = excluded.number,
			volume = excluded.volume, position = excluded.position
		RETURNING id`
	stmt, err := tx.PrepareContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer stmt.Close()

	var added []Chapter
	for i, c := range chs {
		var id int64
		if err := stmt.QueryRowContext(ctx, seriesID, c.URL, c.Name, c.Number, c.Volume, i).Scan(&id); err != nil {
			return nil, err
		}
		if !known[c.URL] {
			c.ID, c.SeriesID, c.Position, c.State = id, seriesID, i, ChapterPending
			added = append(added, c)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return added, nil
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
		SELECT id, module_id, module_name, url, title, cover_url, authors,
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
		if err := rows.Scan(&v.ID, &v.ModuleID, &v.ModuleName, &v.URL, &v.Title,
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
