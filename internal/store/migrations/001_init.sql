-- +goose Up
CREATE TABLE series (
    id          INTEGER PRIMARY KEY,
    module_id   TEXT    NOT NULL,
    module_name TEXT    NOT NULL,
    url         TEXT    NOT NULL,
    title       TEXT    NOT NULL,
    cover_url   TEXT    NOT NULL DEFAULT '',
    authors     TEXT    NOT NULL DEFAULT '',
    artists     TEXT    NOT NULL DEFAULT '',
    genres      TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL DEFAULT '',
    summary     TEXT    NOT NULL DEFAULT '',
    subscribed  INTEGER NOT NULL DEFAULT 1,
    checked_at  DATETIME,
    created_at  DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    UNIQUE (module_id, url)
);

CREATE TABLE chapters (
    id        INTEGER PRIMARY KEY,
    series_id INTEGER NOT NULL REFERENCES series (id) ON DELETE CASCADE,
    url       TEXT    NOT NULL,
    name      TEXT    NOT NULL,
    number    TEXT    NOT NULL DEFAULT '',
    volume    TEXT    NOT NULL DEFAULT '',
    -- pending | queued | downloading | done | failed
    state     TEXT    NOT NULL DEFAULT 'pending',
    file_path TEXT    NOT NULL DEFAULT '',
    error     TEXT    NOT NULL DEFAULT '',
    pages     INTEGER NOT NULL DEFAULT 0,
    position  INTEGER NOT NULL DEFAULT 0,
    UNIQUE (series_id, url)
);
CREATE INDEX idx_chapters_series ON chapters (series_id, position);
CREATE INDEX idx_chapters_state ON chapters (state);

-- The job queue. Claiming happens in a transaction, so a single SQLite file
-- safely serves several worker goroutines without a separate broker.
CREATE TABLE jobs (
    id           INTEGER PRIMARY KEY,
    kind         TEXT    NOT NULL,
    payload      TEXT    NOT NULL DEFAULT '{}',
    -- pending | running | done | failed
    state        TEXT    NOT NULL DEFAULT 'pending',
    attempts     INTEGER NOT NULL DEFAULT 0,
    max_attempts INTEGER NOT NULL DEFAULT 3,
    run_after    DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    error        TEXT    NOT NULL DEFAULT '',
    created_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    updated_at   DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX idx_jobs_claim ON jobs (state, run_after);

-- Values a module declared through AddOption*, overridden by the operator.
CREATE TABLE module_options (
    module_id TEXT NOT NULL,
    name      TEXT NOT NULL,
    value     TEXT NOT NULL,
    PRIMARY KEY (module_id, name)
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

-- +goose Down
DROP TABLE settings;
DROP TABLE module_options;
DROP TABLE jobs;
DROP TABLE chapters;
DROP TABLE series;
