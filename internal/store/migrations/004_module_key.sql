-- +goose Up
-- A module is identified by its file name; the Name it declares in Init() is a
-- label and 145 of 597 modules declare one that differs. Storing the label and
-- looking modules up by it meant every refresh, download and settings lookup
-- failed for those sites once a series was tracked.
ALTER TABLE series ADD COLUMN module_key TEXT NOT NULL DEFAULT '';
UPDATE series SET module_key = module_name WHERE module_key = '';

-- +goose Down
ALTER TABLE series DROP COLUMN module_key;
