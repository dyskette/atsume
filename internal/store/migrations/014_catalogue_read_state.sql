-- +goose Up
-- What a read leaves behind for the next one, and for the page describing it.
--
-- pages is how many directory pages the last complete read took, which is
-- the best estimate of how long the next will take when the module does not
-- say. steps is how many the latest read got through, whatever its end.
ALTER TABLE site_catalogue ADD COLUMN pages INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_catalogue ADD COLUMN steps INTEGER NOT NULL DEFAULT 0;
-- Where an unfinished read stopped, so it can carry on rather than start
-- over: resume_dir is -1 when there is nothing to resume.
ALTER TABLE site_catalogue ADD COLUMN resume_dir  INTEGER NOT NULL DEFAULT -1;
ALTER TABLE site_catalogue ADD COLUMN resume_page INTEGER NOT NULL DEFAULT 0;
-- When the site last read all the way through; built_at moves with every
-- read, failed ones included.
ALTER TABLE site_catalogue ADD COLUMN ok_at DATETIME;
-- The automatic retry waiting for a site that was down or unreachable, and
-- how many have been tried.
ALTER TABLE site_catalogue ADD COLUMN retry_at DATETIME;
ALTER TABLE site_catalogue ADD COLUMN retries  INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE site_catalogue DROP COLUMN retries;
ALTER TABLE site_catalogue DROP COLUMN retry_at;
ALTER TABLE site_catalogue DROP COLUMN ok_at;
ALTER TABLE site_catalogue DROP COLUMN resume_page;
ALTER TABLE site_catalogue DROP COLUMN resume_dir;
ALTER TABLE site_catalogue DROP COLUMN steps;
ALTER TABLE site_catalogue DROP COLUMN pages;
