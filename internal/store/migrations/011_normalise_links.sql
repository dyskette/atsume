-- +goose Up
-- Links are now stored the way FMD2 stores them, with a leading slash on
-- anything relative, because a module is handed its own link back and builds
-- requests from it by concatenation. Rows written before that rule have to
-- move with it: otherwise the next check stores the corrected link as a new
-- chapter and leaves the broken one behind, and a followed series stops
-- matching its own entry in a site's catalogue.
--
-- Absolute addresses are left alone, exactly as the rule in the code leaves
-- them alone.
UPDATE chapters
SET url = '/' || url
WHERE url <> '' AND substr(url, 1, 1) <> '/' AND instr(url, '://') = 0;

UPDATE series
SET url = '/' || url
WHERE url <> '' AND substr(url, 1, 1) <> '/' AND instr(url, '://') = 0;

UPDATE site_title
SET url = '/' || url
WHERE url <> '' AND substr(url, 1, 1) <> '/' AND instr(url, '://') = 0;

-- +goose Down
-- The original shape is not worth recovering: it is the shape that broke.
SELECT 1;
