-- +goose Up
-- A site's title list was fetched and thrown away on every visit. For a
-- module that pages the site internally and returns everything in one call —
-- MangaToon takes three minutes to hand back 2,677 titles — that means three
-- minutes of someone else's bandwidth for each look, and a reader who cannot
-- leave the page without losing it.
--
-- The catalogue is kept, with the date it was read, so the interface can say
-- how old it is instead of pretending freshness it has not earned.
CREATE TABLE site_catalogue (
    site     TEXT    PRIMARY KEY,
    built_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
    -- A crawl that was interrupted leaves a usable but partial list, and the
    -- reader is told which they are looking at.
    complete INTEGER NOT NULL DEFAULT 0,
    -- What the last read reported, for a status line that does not have to
    -- count rows to describe itself.
    note     TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE site_title (
    site TEXT    NOT NULL REFERENCES site_catalogue (site) ON DELETE CASCADE,
    url  TEXT    NOT NULL,
    name TEXT    NOT NULL,
    -- The order the site listed it in, which is usually meaningful.
    seq  INTEGER NOT NULL,
    PRIMARY KEY (site, url)
);
CREATE INDEX idx_site_title_seq ON site_title (site, seq);

-- +goose Down
DROP TABLE site_title;
DROP TABLE site_catalogue;
