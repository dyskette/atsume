-- +goose Up
-- What the site page needs to explain a read in plain words rather than
-- quote the module: the last HTTP status, whether an anti-bot page was seen,
-- why a site that did not answer did not (a name that no longer resolves, a
-- refused connection, a timeout), and the address a request to the site was
-- redirected to on another host, which is how a site that moved says where.
ALTER TABLE site_catalogue ADD COLUMN status     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_catalogue ADD COLUMN challenged INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_catalogue ADD COLUMN cause      TEXT    NOT NULL DEFAULT '';
ALTER TABLE site_catalogue ADD COLUMN moved_to   TEXT    NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE site_catalogue DROP COLUMN moved_to;
ALTER TABLE site_catalogue DROP COLUMN cause;
ALTER TABLE site_catalogue DROP COLUMN challenged;
ALTER TABLE site_catalogue DROP COLUMN status;
