-- +goose Up
-- A failed read kept only its error message, which says what went wrong in
-- the module's words. What kind of failure it was — the site blocked
-- atsume, moved, is down, cannot be reached, or the module broke — decides
-- what a reader can do about it, so it is recorded on its own.
ALTER TABLE site_catalogue ADD COLUMN problem TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE site_catalogue DROP COLUMN problem;
