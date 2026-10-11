-- +goose Up
-- What the library needs to explain a failed series check in plain words,
-- as the site catalogue already records for a failed read: the last HTTP
-- status, whether an anti-bot page was seen, and why a site that did not
-- answer did not ("dns", "refused", "timeout"). The kind alone cannot tell a
-- challenge page from a plain 403, and the fix differs. Cleared with the
-- rest of the check failure by the next check that works.
ALTER TABLE series ADD COLUMN check_status     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE series ADD COLUMN check_challenged INTEGER NOT NULL DEFAULT 0;
ALTER TABLE series ADD COLUMN check_cause      TEXT    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE series DROP COLUMN check_cause;
ALTER TABLE series DROP COLUMN check_challenged;
ALTER TABLE series DROP COLUMN check_status;
