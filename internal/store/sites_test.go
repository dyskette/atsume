package store

import (
	"context"
	"testing"
	"time"
)

// TestSiteUsage covers what the Sites page's cards read: series and follows
// per site, chapters that arrived today, and a chapter check that failed.
func TestSiteUsage(t *testing.T) {
	st, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := st.DB.ExecContext(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`INSERT INTO series (id, module_id, module_name, module_key, url, title, subscribed) VALUES
		(1, 'a', 'Alpha', 'Alpha', '/one/', 'One', 1),
		(2, 'a', 'Alpha', 'Alpha', '/two/', 'Two', 0),
		(3, 'b', 'Beta', '', '/three/', 'Three', 1)`)
	exec(`INSERT INTO chapters (series_id, url, name, arrived_at, backlog) VALUES
		(1, '/one/1', 'c1', datetime('now'), 0),
		(1, '/one/2', 'c2', datetime('now', '-3 days'), 0),
		(2, '/two/1', 'c1', datetime('now'), 1)`)
	exec(`INSERT INTO jobs (kind, payload, state, error, updated_at) VALUES
		('refresh_series', '{"module":"Beta","url":"/three/"}', 'failed', 'network problem', datetime('now'))`)

	usage, err := st.SiteUsage(ctx, time.Now().Add(-12*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	a, b := usage["Alpha"], usage["Beta"]
	if a.Series != 2 || a.Following != 1 || a.NewToday != 1 {
		t.Errorf("Alpha = %+v, want 2 series, 1 followed, 1 new today (the backlog chapter is not news)", a)
	}
	if b.Series != 1 || b.CheckError != "network problem" {
		t.Errorf("Beta = %+v, want its failed check, found by label", b)
	}
}
