-- +goose Up
-- Marking titles with the time a read saw them was wrong: SQLite's
-- CURRENT_TIMESTAMP has one-second resolution, so a title written in the
-- same second the read began compared as older than the read and survived a
-- prune it should not have. A read that takes milliseconds — a small site,
-- or a test — kept titles the site had dropped.
--
-- A counter cannot tie. Each read takes the next number and stamps what it
-- sees; the prune removes whatever carries an older one.
ALTER TABLE site_catalogue ADD COLUMN read_seq INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_title ADD COLUMN seen_read INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE site_title DROP COLUMN seen_read;
ALTER TABLE site_catalogue DROP COLUMN read_seq;
