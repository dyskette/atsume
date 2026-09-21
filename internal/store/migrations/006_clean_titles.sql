-- +goose Up
-- Scraped titles arrive with the indentation of the page they were cut from.
-- That was invisible while the library was one flat list, and became a bug
-- the moment the list was grouped and sorted: a title starting with a newline
-- sorts before every letter, so the rows a reader looks for moved to the top
-- of whichever section they landed in.
UPDATE series SET title = trim(title, char(32) || char(9) || char(10) || char(13));

-- A check that scraped no title used to overwrite the one the listing gave
-- us, leaving a row with nothing to click.
UPDATE series SET title = url WHERE title = '';

-- +goose Down
-- The original whitespace is not recoverable, and would not be wanted.
SELECT 1;
