package store

import (
	"context"
	"database/sql"
	"time"
)

// SiteUse is how a site figures in the library, for the Sites page's cards.
type SiteUse struct {
	// Series is how many series from the site are in the library, and
	// Following how many of those are followed.
	Series, Following int
	// NewToday is how many chapters arrived since the start of the day.
	NewToday int
	// CheckedAt is when a series from the site was last checked.
	CheckedAt time.Time
	// CheckError is the latest chapter check of the site's series that
	// failed within the last day, "" when none did.
	CheckError string
}

// SiteUsage reports, by site, the sites the library uses. dayStart is the
// start of the reader's day, for chapters that arrived today.
func (s *Store) SiteUsage(ctx context.Context, dayStart time.Time) (map[string]SiteUse, error) {
	out := map[string]SiteUse{}
	// Rows written before the key and the label were told apart hold the
	// label in module_name, which is also the site's name.
	rows, err := s.DB.QueryContext(ctx, `
		SELECT COALESCE(NULLIF(s.module_key, ''), s.module_name), COUNT(*), SUM(s.subscribed), MAX(s.checked_at),
		       (SELECT COUNT(*) FROM chapters c JOIN series t ON t.id = c.series_id
		        WHERE COALESCE(NULLIF(t.module_key, ''), t.module_name) = COALESCE(NULLIF(s.module_key, ''), s.module_name)
		          AND c.backlog = 0 AND julianday(c.arrived_at) >= julianday(?))
		FROM series s GROUP BY 1`, sqlTime(dayStart))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var site string
		var u SiteUse
		var checked sql.NullString
		if err := rows.Scan(&site, &u.Series, &u.Following, &checked, &u.NewToday); err != nil {
			return nil, err
		}
		if t := parseTimestamp(checked); t.Valid {
			u.CheckedAt = t.Time
		}
		out[site] = u
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// A failed check lives only on its job: the error of the latest attempt,
	// whether it gave up or is waiting to try again.
	fails, err := s.DB.QueryContext(ctx, `
		SELECT json_extract(payload, '$.module'), error FROM jobs
		WHERE kind = 'refresh_series' AND error <> ''
		  AND (state = 'failed' OR state = 'pending')
		  AND julianday(updated_at) > julianday('now', '-1 day')
		ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer fails.Close()
	for fails.Next() {
		var site sql.NullString
		var msg string
		if err := fails.Scan(&site, &msg); err != nil {
			return nil, err
		}
		if u, ok := out[site.String]; ok {
			u.CheckError = msg
			out[site.String] = u
		}
	}
	return out, fails.Err()
}
