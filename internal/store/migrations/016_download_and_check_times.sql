-- +goose Up
-- When a chapter's file was written. The library wants to say what came in
-- recently, and a chapter row knew whether it was on disk but not since when.
-- Chapters already downloaded keep NULL: there is no honest way to recover
-- the moment, and a guess would sort them as if it were known.
ALTER TABLE chapters ADD COLUMN downloaded_at DATETIME;

-- Why the last check of a series failed, and when. A failed check used to
-- leave the row untouched, so a series whose site had broken looked exactly
-- like one that was simply quiet. check_problem is the kind of failure, as
-- the site catalogue records it, because the fix differs (a broken reader is
-- not a site that is down) and the error text alone cannot be classified
-- after the fact. All three are cleared by the next check that works.
ALTER TABLE series ADD COLUMN check_problem   TEXT NOT NULL DEFAULT '';
ALTER TABLE series ADD COLUMN check_error     TEXT NOT NULL DEFAULT '';
ALTER TABLE series ADD COLUMN check_failed_at DATETIME;

-- +goose Down
ALTER TABLE series DROP COLUMN check_failed_at;
ALTER TABLE series DROP COLUMN check_error;
ALTER TABLE series DROP COLUMN check_problem;
ALTER TABLE chapters DROP COLUMN downloaded_at;
