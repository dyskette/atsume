package scraper

import (
	"context"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// scrapeRunner is what the golden and recorded tests drive, satisfied by both
// Runner and goluaRunner while the port is in progress.
type scrapeRunner interface {
	Close()
	Module() *Module
	Sites() []*Module
	HasHandler(event string) bool
	GetNameAndLink(page int) ([]Entry, error)
	GetInfo(mangaURL string) (*MangaInfo, error)
	GetPageNumber(chapterURL string) ([]string, error)

	// testGlobal reads a global as text, and testCall runs an event's handler
	// and reports only whether it failed; tests use them to look inside.
	testGlobal(name string) string
	testCall(event string) error
}

func (r *Runner) testGlobal(name string) string { return r.L.GetGlobal(name).String() }

func (r *Runner) testCall(event string) error {
	_, err := r.call(event)
	return err
}

func (gr *goluaRunner) testGlobal(name string) string {
	s, _ := gr.r.GlobalEnv().Get(rt.StringValue(name)).ToString()
	return s
}

func (gr *goluaRunner) testCall(event string) error {
	_, err := gr.call(event)
	return err
}

// onGoluaOpen, when set, sees every runner the golua runtime opens, so a test
// can inspect runners that other test helpers create and close.
var onGoluaOpen func(*goluaRunner)

// eachRuntime runs fn once per Lua runtime, as a subtest named after it.
func eachRuntime(t *testing.T, fn func(t *testing.T, lr luaRuntime)) {
	for _, lr := range luaRuntimes {
		t.Run(lr.name, func(t *testing.T) { fn(t, lr) })
	}
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
		if onGoluaOpen != nil {
			onGoluaOpen(r)
		}
		return r, nil
	}},
}
