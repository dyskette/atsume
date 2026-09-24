package scraper

import "context"

// scrapeRunner is what the golden and recorded tests drive, satisfied by both
// Runner and goluaRunner while the port is in progress.
type scrapeRunner interface {
	Close()
	GetNameAndLink(page int) ([]Entry, error)
	GetInfo(mangaURL string) (*MangaInfo, error)
	GetPageNumber(chapterURL string) ([]string, error)
}

// luaRuntime opens a module with one of the two Lua runtimes.
type luaRuntime struct {
	name string
	// writes is whether this runtime may rewrite goldens and recordings. Only
	// gopher-lua does, so golua is always checked against its output.
	writes bool
	open   func(h *Host, ctx context.Context, file, site, rootURL string) (scrapeRunner, error)
}

var luaRuntimes = []luaRuntime{
	{name: "gopher-lua", writes: true, open: func(h *Host, ctx context.Context, file, site, rootURL string) (scrapeRunner, error) {
		r, err := h.Open(ctx, file, site, rootURL)
		if err != nil {
			return nil, err
		}
		return r, nil
	}},
	{name: "golua", open: func(h *Host, ctx context.Context, file, site, rootURL string) (scrapeRunner, error) {
		r, err := h.openGolua(ctx, file, site, rootURL)
		if err != nil {
			return nil, err
		}
		return r, nil
	}},
}
