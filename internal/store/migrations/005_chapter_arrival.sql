-- +goose Up
-- A library that cannot say what is new cannot answer the question it exists
-- for. Following promises that new chapters arrive by themselves, but a
-- chapter published last night was recorded exactly like one of the hundred
-- that were already there when the series was followed, so the library sorted
-- both into the same alphabetical wall.
--
-- The distinction was known at check time and thrown away. These columns keep
-- it: backlog marks what was already published when the series was first
-- checked, and arrived_at is when atsume first saw the chapter.
ALTER TABLE chapters ADD COLUMN arrived_at DATETIME;
ALTER TABLE chapters ADD COLUMN backlog INTEGER NOT NULL DEFAULT 0;

-- Everything already recorded predates the distinction, and there is no
-- honest way to recover it. Calling it backlog is the conservative reading:
-- a wrongly quiet row costs a reader one click, a hundred wrongly "new" rows
-- cost them the section.
UPDATE chapters SET backlog = 1;

CREATE INDEX idx_chapters_arrival ON chapters (series_id, backlog, state);

-- +goose Down
DROP INDEX idx_chapters_arrival;
ALTER TABLE chapters DROP COLUMN backlog;
ALTER TABLE chapters DROP COLUMN arrived_at;
