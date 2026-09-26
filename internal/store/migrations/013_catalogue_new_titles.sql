-- +goose Up
-- A refresh finds titles the site added since the last read, and those are
-- what a reader coming back wants to see first. Each title records the read
-- that first saw it, and the catalogue which read's new titles count: the
-- current one, when it began with titles already stored. The very first read
-- of a site finds everything, which is not the same as everything being new.
ALTER TABLE site_title ADD COLUMN first_read INTEGER NOT NULL DEFAULT 0;
ALTER TABLE site_catalogue ADD COLUMN new_from INTEGER NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE site_catalogue DROP COLUMN new_from;
ALTER TABLE site_title DROP COLUMN first_read;
