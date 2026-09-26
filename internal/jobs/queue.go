package jobs

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"
)

// Job kinds.
const (
	KindRefreshSeries   = "refresh_series"
	KindDownloadChapter = "download_chapter"
	KindIndexSite       = "index_site"
)

// Job is one unit of queued work.
type Job struct {
	ID          int64
	Kind        string
	Payload     json.RawMessage
	Attempts    int
	MaxAttempts int
}

// Queue is a SQLite-backed work queue.
type Queue struct{ db *sql.DB }

// NewQueue wraps a database handle.
func NewQueue(db *sql.DB) *Queue { return &Queue{db: db} }

// Enqueue adds a job to run as soon as a worker is free.
func (q *Queue) Enqueue(ctx context.Context, kind string, payload any) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	const stmt = `INSERT INTO jobs (kind, payload) VALUES (?, ?) RETURNING id`
	var id int64
	err = q.db.QueryRowContext(ctx, stmt, kind, string(raw)).Scan(&id)
	return id, err
}

// EnqueueAt adds a job that no worker takes before at.
func (q *Queue) EnqueueAt(ctx context.Context, kind string, payload any, at time.Time) (int64, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	var id int64
	err = q.db.QueryRowContext(ctx,
		`INSERT INTO jobs (kind, payload, run_after) VALUES (?, ?, ?) RETURNING id`,
		kind, string(raw), sqlTime(at)).Scan(&id)
	return id, err
}

// Claim atomically takes the next runnable job, or returns nil when there is
// none. The UPDATE ... RETURNING runs as a single statement so two workers can
// never claim the same row.
func (q *Queue) Claim(ctx context.Context) (*Job, error) {
	const stmt = `
		UPDATE jobs SET state = 'running', attempts = attempts + 1, updated_at = CURRENT_TIMESTAMP
		WHERE id = (
			SELECT id FROM jobs
			WHERE state = 'pending' AND run_after <= CURRENT_TIMESTAMP
			ORDER BY id LIMIT 1
		)
		RETURNING id, kind, payload, attempts, max_attempts`
	var j Job
	var payload string
	err := q.db.QueryRowContext(ctx, stmt).Scan(&j.ID, &j.Kind, &payload, &j.Attempts, &j.MaxAttempts)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	j.Payload = json.RawMessage(payload)
	return &j, nil
}

// Complete marks a job finished.
func (q *Queue) Complete(ctx context.Context, id int64) error {
	_, err := q.db.ExecContext(ctx,
		`UPDATE jobs SET state = 'done', error = '', updated_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// Fail records an error, rescheduling with backoff while attempts remain.
func (q *Queue) Fail(ctx context.Context, j *Job, cause error) error {
	if j.Attempts >= j.MaxAttempts {
		_, err := q.db.ExecContext(ctx,
			`UPDATE jobs SET state = 'failed', error = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
			cause.Error(), j.ID)
		return err
	}
	// Exponential backoff: these are third-party sites, so a failure is as
	// likely to be rate limiting as a bug.
	delay := time.Duration(1<<uint(j.Attempts)) * time.Minute
	_, err := q.db.ExecContext(ctx,
		`UPDATE jobs SET state = 'pending', error = ?, run_after = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		cause.Error(), sqlTime(time.Now().Add(delay)), j.ID)
	return err
}

// PendingOfKind returns the payloads of jobs of one kind that have not
// finished, so a caller can avoid queueing the same work twice.
func (q *Queue) PendingOfKind(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT payload FROM jobs WHERE kind = ? AND state IN ('pending','running')`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

// DeletePending removes the jobs of one kind that have not started and whose
// payload has key set to value, and reports how many it removed. A job
// already running is left alone; stopping it is the handler's business.
func (q *Queue) DeletePending(ctx context.Context, kind, key string, value any) (int64, error) {
	res, err := q.db.ExecContext(ctx,
		`DELETE FROM jobs WHERE kind = ? AND state = 'pending' AND json_extract(payload, '$.' || ?) = ?`,
		kind, key, value)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ReadyOfKind returns the payloads of jobs of one kind waiting to start and
// ready to, leaving out those waiting for a retry time still to come.
func (q *Queue) ReadyOfKind(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT payload FROM jobs WHERE kind = ? AND state = 'pending' AND run_after <= CURRENT_TIMESTAMP`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []json.RawMessage
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(p))
	}
	return out, rows.Err()
}

// PendingInOrder returns the payloads of jobs of one kind waiting to start,
// in the order workers will take them: those ready now by age, then those
// waiting out a retry delay.
func (q *Queue) PendingInOrder(ctx context.Context, kind string) ([]json.RawMessage, error) {
	rows, err := q.db.QueryContext(ctx,
		`SELECT payload FROM jobs WHERE kind = ? AND state = 'pending'
		 ORDER BY run_after > CURRENT_TIMESTAMP, id`, kind)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []json.RawMessage
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		out = append(out, json.RawMessage(raw))
	}
	return out, rows.Err()
}

// Stats counts jobs by state, for the dashboard and /healthz.
func (q *Queue) Stats(ctx context.Context) (map[string]int, error) {
	rows, err := q.db.QueryContext(ctx, `SELECT state, COUNT(*) FROM jobs GROUP BY state`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := map[string]int{}
	for rows.Next() {
		var state string
		var n int
		if err := rows.Scan(&state, &n); err != nil {
			return nil, err
		}
		out[state] = n
	}
	return out, rows.Err()
}

// ResetRunning returns jobs abandoned by a crash to the pending state and
// reports how many were requeued. It runs at startup, because a 'running' row
// whose process is gone would otherwise sit there forever.
func (q *Queue) ResetRunning(ctx context.Context) (int64, error) {
	res, err := q.db.ExecContext(ctx,
		`UPDATE jobs SET state = 'pending' WHERE state = 'running'`)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// sqlTime formats t the way SQLite's CURRENT_TIMESTAMP does, in UTC, so the
// two compare as text. The driver's own format for a time.Time carries a "T"
// and a local offset, and compared against CURRENT_TIMESTAMP that made a
// retry wait until midnight UTC or run at once, depending on the hour.
func sqlTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }
