package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// moduleCheckout writes an FMD2-shaped checkout holding one module that
// reads the given site.
func moduleCheckout(t *testing.T, root string) string {
	t.Helper()
	dir := t.TempDir()
	mods := filepath.Join(dir, "lua", "modules")
	if err := os.MkdirAll(mods, 0o755); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`
function Init()
	local m = NewWebsiteModule()
	m.ID = 'test'
	m.Name = 'Tester'
	m.RootURL = '%s'
	m.OnGetNameAndLink = 'GetNameAndLink'
	m.OnGetInfo = 'GetInfo'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetNameAndLink()
	if not HTTP.GET(MODULE.RootURL .. '/list?page=' .. (URL + 1)) then return net_problem end
	local x = CreateTXQuery(HTTP.Document)
	x.XPathHREFAll('//a[@class="t"]', LINKS, NAMES)
	UPDATELIST.CurrentDirectoryPageNumber = 7
	return no_error
end

function GetInfo()
	if not HTTP.GET(MaybeFillHost(MODULE.RootURL, URL)) then return net_problem end
	local x = CreateTXQuery(HTTP.Document)
	MANGAINFO.Title = x.XPathString('//h1')
	x.XPathHREFAll('//a[@class="c"]', MANGAINFO.ChapterLinks, MANGAINFO.ChapterNames)
	return no_error
end

function GetPageNumber()
	TASK.PageLinks.Add(MODULE.RootURL .. '/img/1.png')
	return true
end
`, root)
	if err := os.WriteFile(filepath.Join(mods, "Tester.lua"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<a class="t" href="/s/one/">One</a><a class="t" href="/s/two/">Two</a>`)
	})
	mux.HandleFunc("/s/one/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<h1>One</h1><a class="c" href="/s/one/1/">Chapter 1</a>`)
	})
	mux.HandleFunc("/img/1.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("png!"))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestModuleCommand(t *testing.T) {
	srv := testSite(t)
	fmd2 := moduleCheckout(t, srv.URL)
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := runModule(append([]string{"-fmd2", fmd2}, args...), &out)
		return out.String(), err
	}
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"list", []string{"Tester", "list"}, []string{"2 titles · the module reports 7 pages", "/s/one/\tOne", "/s/two/\tTwo"}},
		{"info", []string{"Tester", "info", "/s/one/"}, []string{"Title      One", "Summary    (empty)", "1 chapters", "/s/one/1/\tChapter 1"}},
		// A flag after the arguments counts as much as one before them.
		{"pages", []string{"Tester", "pages", "/s/one/1/", "-fetch-first"}, []string{"1 pages", "first image: HTTP 200 · image/png · 4 bytes"}},
		{"xpath", []string{"xpath", srv.URL + "/list", `//a[@class="t"]/@href`}, []string{"HTTP 200 · 2 results", "/s/two/"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := run(c.args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			for _, w := range c.want {
				if !strings.Contains(out, w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}

	if _, err := run("Missing", "list"); err == nil || !strings.Contains(err.Error(), "no module file") {
		t.Errorf("a missing module: %v", err)
	}
	if _, err := run("xpath", srv.URL+"/list", "//a[contains(@class,"); err == nil {
		t.Error("a broken expression should be reported, not come back empty")
	}
}
