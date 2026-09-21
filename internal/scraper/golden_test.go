package scraper

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files from the current output")

// goldenInfo is the extracted metadata a golden file pins.
type goldenInfo struct {
	Title        string   `json:"title"`
	AltTitles    string   `json:"alt_titles"`
	CoverLink    string   `json:"cover_link"`
	Authors      string   `json:"authors"`
	Artists      string   `json:"artists"`
	Genres       string   `json:"genres"`
	Status       string   `json:"status"`
	Summary      string   `json:"summary"`
	ChapterLinks []string `json:"chapter_links"`
	ChapterNames []string `json:"chapter_names"`
}

// goldenResult is the whole output of running a module against its fixtures.
type goldenResult struct {
	Template  string      `json:"template"`
	Directory []Entry     `json:"directory,omitempty"`
	Info      *goldenInfo `json:"info"`
	Pages     []string    `json:"pages"`
}

// goldenCase describes one template under test. The module is written here
// rather than copied from upstream so that no GPL-2.0-only module file enters
// this repository; it delegates to the real upstream template, which is what
// the golden actually pins.
type goldenCase struct {
	name string
	// template is the upstream template the module delegates to.
	template string
	// routes maps a URL path to a fixture file in the case directory.
	routes map[string]string
	// seriesPath and chapterPath are what the handlers are pointed at.
	seriesPath  string
	chapterPath string
	// directory is whether the case exercises GetNameAndLink.
	directory bool
	// needsChainedForIn marks a template that iterates with the
	// `x.XPath(expr).Get()` idiom, which an unpatched runtime miscompiles.
	needsChainedForIn bool

	// rootURL, when set, replaces the test server: the module talks to fixed
	// API hosts instead, and exchanges seeds a cassette with the responses.
	// Templates built on a separate API host cannot be pointed at a local
	// server, because they derive the API address from RootURL or hard-code it.
	rootURL   string
	exchanges []goldenExchange
	// module is the Lua source, with %s replaced by the server URL.
	module string
}

var goldenCases = []goldenCase{
	{
		name:              "mangahub",
		template:          "MangaHub",
		rootURL:           "https://mangahub.example.test",
		seriesPath:        "/manga/vagabond",
		chapterPath:       "/vagabond/chapter-1",
		needsChainedForIn: false,
		exchanges: []goldenExchange{
			{method: "POST", url: "https://api.mghcdn.com/graphql", contains: "{manga(", file: "info.json"},
			{method: "POST", url: "https://api.mghcdn.com/graphql", contains: "{chapter(", file: "pages.json"},
		},
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'c3d4e5f60718293a4b5c6d7e8f901234'
	m.Name            = 'GoldenHub'
	m.RootURL         = '%s'
	m.Category        = 'English'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
end

-- Variables is a per-site constant the module supplies, not the template.
Variables = 'mn03'

local Template = require 'templates.MangaHub'

function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`,
	},
	{
		name:              "vtheme",
		template:          "VTheme",
		rootURL:           "https://vtheme.example.test",
		seriesPath:        "/series/blood-and-steel",
		chapterPath:       "/series/blood-and-steel/9001",
		needsChainedForIn: true,
		exchanges: []goldenExchange{
			{method: "GET", url: "https://vtheme.example.test/series/blood-and-steel", file: "series.html"},
			{method: "GET", url: "https://api.vtheme.example.test/api/post?postId=12345", file: "post.json"},
			{method: "GET", url: "https://api.vtheme.example.test/api/chapter?chapterId=9001", file: "chapter.json"},
		},
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'd4e5f60718293a4b5c6d7e8f90123456'
	m.Name            = 'GoldenVTheme'
	m.RootURL         = '%s'
	m.Category        = 'English'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
	m.AddOptionCheckBox('showpaidchapters', 'Show paid chapters', false)
end

local Template = require 'templates.VTheme'

function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`,
	},
	{
		name:     "mangareaderonline",
		template: "MangaReaderOnline",
		routes: map[string]string{
			"/berserk":     "series.html",
			"/berserk/374": "chapter.html",
		},
		seriesPath:        "/berserk",
		chapterPath:       "/berserk/374",
		needsChainedForIn: true,
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'a1b2c3d4e5f60718293a4b5c6d7e8f90'
	m.Name            = 'GoldenReaderOnline'
	m.RootURL         = '%s'
	m.Category        = 'English'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
end

local Template = require 'templates.MangaReaderOnline'

function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`,
	},
	{
		name:     "keyoapp",
		template: "KeyoApp",
		routes: map[string]string{
			"/series/orv":           "series.html",
			"/series/orv/chapter-1": "chapter.html",
		},
		seriesPath:        "/series/orv",
		chapterPath:       "/series/orv/chapter-1",
		needsChainedForIn: true,
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'b2c3d4e5f60718293a4b5c6d7e8f9012'
	m.Name            = 'GoldenKeyo'
	m.RootURL         = '%s'
	m.Category        = 'English'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
	m.AddOptionCheckBox('showpaidchapters', 'Show paid chapters', false)
end

local Template = require 'templates.KeyoApp'

function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`,
	},
	{
		name:     "madara",
		template: "Madara",
		routes: map[string]string{
			"/wp-admin/admin-ajax.php":        "directory.html",
			"/manga/solo-leveling/":           "series.html",
			"/manga/solo-leveling/chapter-1/": "chapter.html",
		},
		seriesPath:  "/manga/solo-leveling/",
		chapterPath: "/manga/solo-leveling/chapter-1/",
		directory:   true,
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID                       = '46e0c618a19748d6af150c2f198f5360'
	m.Name                     = 'GoldenMadara'
	m.RootURL                  = '%s'
	m.Category                 = 'English'
	m.OnGetNameAndLink         = 'GetNameAndLink'
	m.OnGetInfo                = 'GetInfo'
	m.OnGetPageNumber          = 'GetPageNumber'
end

local Template = require 'templates.Madara'

function GetNameAndLink() Template.GetNameAndLink() return no_error end
function GetInfo()        Template.GetInfo()        return no_error end
function GetPageNumber()  return Template.GetPageNumber() end
`,
	},
	{
		name:     "mangathemesia",
		template: "MangaThemesia",
		routes: map[string]string{
			"/manga/kanojo/":     "series.html",
			"/kanojo-chapter-1/": "chapter.html",
		},
		seriesPath:        "/manga/kanojo/",
		chapterPath:       "/kanojo-chapter-1/",
		needsChainedForIn: true,
		module: `
function Init()
	local m = NewWebsiteModule()
	m.ID              = '7f8e1ad2b1f34c5f9d0e6a3b2c4d5e6f'
	m.Name            = 'GoldenThemesia'
	m.RootURL         = '%s'
	m.Category        = 'English'
	m.OnGetInfo       = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
end

local Template = require 'templates.MangaThemesia'

function GetInfo()       Template.GetInfo()       return no_error end
function GetPageNumber() return Template.GetPageNumber() end
`,
	},
}

// TestGolden runs each template against recorded-shape fixtures and compares
// every extracted field with a committed golden file.
//
// The fixtures deliberately carry what hand-written HTML in a Go string usually
// does not: HTML entities, non-breaking spaces, unclosed <li> tags, lazy-load
// placeholder images alongside the real ones, ad rows that must be skipped, and
// CDN prefixes the template is expected to strip.
func TestGolden(t *testing.T) {
	luaRoot := luaDir(t)

	for _, c := range goldenCases {
		t.Run(c.name, func(t *testing.T) {
			if c.needsChainedForIn && runtimeForInBug() {
				t.Skip("blocked by the gopher-lua generic-for bug; see TestRuntimeSupportsChainedForIn")
			}
			caseDir := filepath.Join("testdata", "golden", c.name)

			var base string
			var hostAddr string
			var transport http.RoundTripper
			if c.rootURL != "" {
				base, hostAddr = c.rootURL, strings.TrimPrefix(c.rootURL, "https://")
				transport = seedCassette(t, caseDir, c.exchanges)
			} else {
				var srv *httptest.Server
				srv, base = goldenServer(t, caseDir, c.routes)
				hostAddr = srv.Listener.Addr().String()
			}

			modPath := filepath.Join(t.TempDir(), c.name+".lua")
			if err := os.WriteFile(modPath, []byte(fmt.Sprintf(c.module, base)), 0o644); err != nil {
				t.Fatal(err)
			}

			h := &Host{LuaDir: luaRoot, Transport: transport}
			r, err := h.Open(context.Background(), modPath, "", "")
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()

			got := goldenResult{Template: c.template}

			if c.directory {
				entries, err := r.GetNameAndLink(0)
				if err != nil {
					t.Fatalf("GetNameAndLink: %v", err)
				}
				got.Directory = entries
			}

			info, err := r.GetInfo(base + c.seriesPath)
			if err != nil {
				t.Fatalf("GetInfo: %v", err)
			}
			got.Info = &goldenInfo{
				Title: info.Title, AltTitles: info.AltTitles, CoverLink: info.CoverLink,
				Authors: info.Authors, Artists: info.Artists, Genres: info.Genres,
				Status: info.Status, Summary: info.Summary,
				ChapterLinks: info.ChapterLinks.All(), ChapterNames: info.ChapterNames.All(),
			}

			pages, err := r.GetPageNumber(base + c.chapterPath)
			if err != nil {
				t.Fatalf("GetPageNumber: %v", err)
			}
			got.Pages = pages

			// The test server's port changes every run, so it is folded back
			// into a placeholder before the comparison.
			normalized := strings.ReplaceAll(mustJSON(t, got), base, "{{BASE}}")
			normalized = strings.ReplaceAll(normalized, hostAddr, "{{HOST}}")

			goldenPath := filepath.Join(caseDir, "golden.json")
			if *updateGolden {
				if err := os.WriteFile(goldenPath, []byte(normalized+"\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				t.Logf("wrote %s", goldenPath)
				return
			}

			want, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("%v\nrun `go test ./internal/scraper/ -run TestGolden -update` to create it", err)
			}
			if diff := strings.TrimSpace(string(want)); diff != normalized {
				t.Errorf("output differs from %s\n--- want ---\n%s\n--- got ---\n%s",
					goldenPath, diff, normalized)
			}
		})
	}
}

// goldenExchange is one response seeded into a cassette. A request body is
// never matched, so a query carrying a nonce still replays.
type goldenExchange struct {
	method string
	url    string
	file   string
	// contains selects this response by a substring of the request body, for an
	// endpoint that serves several operations from one URL.
	contains string
}

// seedCassette builds a cassette from a case's declared exchanges.
func seedCassette(t *testing.T, dir string, exchanges []goldenExchange) *Cassette {
	t.Helper()
	c, err := NewCassette(filepath.Join(t.TempDir(), "cassette"), false, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range exchanges {
		body, err := os.ReadFile(filepath.Join(dir, e.file))
		if err != nil {
			t.Fatal(err)
		}
		if e.contains != "" {
			c.PutMatching(e.method, e.url, e.contains, 200, body)
			continue
		}
		if err := c.Put(e.method, e.url, nil, 200, body); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// goldenServer serves the case's fixture files, substituting the live base URL.
func goldenServer(t *testing.T, dir string, routes map[string]string) (*httptest.Server, string) {
	t.Helper()
	var base string
	mux := http.NewServeMux()

	for path, file := range routes {
		file := filepath.Join(dir, file)
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			raw, err := os.ReadFile(file)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			body := strings.ReplaceAll(string(raw), "{{BASE}}", base)
			body = strings.ReplaceAll(body, "{{HOST}}", strings.TrimPrefix(base, "http://"))
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, body)
		})
	}

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv, base
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
