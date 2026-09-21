-- +goose Up
-- A catalogue now has two possible origins: atsume read the site, or it
-- downloaded a snapshot somebody else read. They differ in ways a reader
-- cares about — seconds against minutes, and a snapshot can be years old —
-- so which one this is has to be recorded rather than inferred.
ALTER TABLE site_catalogue ADD COLUMN source TEXT NOT NULL DEFAULT 'site';

-- DataAt is how old the titles are, which for a snapshot is not when it was
-- downloaded. FMD2's snapshots carry a date per row; the newest of them is
-- the honest answer, since a file can be re-committed without its contents
-- changing.
ALTER TABLE site_catalogue ADD COLUMN data_at DATETIME;

-- +goose Down
ALTER TABLE site_catalogue DROP COLUMN data_at;
ALTER TABLE site_catalogue DROP COLUMN source;
