-- +goose Up
-- Re-reading a site used to delete its titles first, so pressing "read
-- again" on a large site meant minutes with no list at all — and a read that
-- failed halfway left the site emptier than before it was pressed.
--
-- Titles are now marked as each read sees them, and only the ones a
-- completed read did not see are removed at the end. The list stays usable
-- throughout, and a failed read costs nothing.
ALTER TABLE site_title ADD COLUMN seen_at DATETIME;
UPDATE site_title SET seen_at = CURRENT_TIMESTAMP;

-- +goose Down
ALTER TABLE site_title DROP COLUMN seen_at;
