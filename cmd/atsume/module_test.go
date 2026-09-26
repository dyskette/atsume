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
	// run returns results and the notes about them together; resultsOf
	// only the results, which is what a pipe gets.
	resultsOf := func(args ...string) (string, string, error) {
		var out, notes bytes.Buffer
		err := runModule(append([]string{"-fmd2", fmd2}, args...), &out, &notes)
		return out.String(), notes.String(), err
	}
	run := func(args ...string) (string, error) {
		out, notes, err := resultsOf(args...)
		return notes + out, err
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

	// A page saved with fetch is what xpath reads without asking the site.
	saved := filepath.Join(t.TempDir(), "list.html")
	if out, err := run("fetch", srv.URL+"/list", "-o", saved); err != nil || !strings.Contains(out, "HTTP 200") {
		t.Fatalf("fetch: %v\n%s", err, out)
	}
	if out, err := run("xpath", saved, `//a[@class="t"]`); err != nil || !strings.Contains(out, "2 results") || !strings.Contains(out, "Two") {
		t.Errorf("xpath on a saved page: %v\n%s", err, out)
	}

	// record writes a case the recorded test can replay; a run that finds
	// nothing writes nothing.
	recorded := t.TempDir()
	out, err := run("Tester", "record", "/s/one/", "/s/one/1/", "-recorded", recorded)
	if err != nil || !strings.Contains(out, "1 chapters · 1 pages") {
		t.Fatalf("record: %v\n%s", err, out)
	}
	for _, f := range []string{"case.json", "golden.json", "cassette"} {
		if _, err := os.Stat(filepath.Join(recorded, "tester", f)); err != nil {
			t.Errorf("record did not write %s: %v", f, err)
		}
	}
	if _, err := run("Tester", "record", "/s/nothing/", "-recorded", recorded, "-name", "empty"); err == nil {
		t.Error("a run that extracts nothing should not be recorded")
	}
	if _, err := os.Stat(filepath.Join(recorded, "empty")); err == nil {
		t.Error("a refused recording left a directory behind")
	}

	// Piped results are only results; counts and statuses go to the notes.
	if out, notes, err := resultsOf("xpath", srv.URL+"/list", `//a[@class="t"]/@href`); err != nil ||
		strings.Contains(out, "results") || !strings.Contains(notes, "2 results") {
		t.Errorf("xpath results %q, notes %q, err %v", out, notes, err)
	}
	if out, _, _ := resultsOf("Tester", "list"); strings.Contains(out, "titles") {
		t.Errorf("list results carry its header: %q", out)
	}
	// -html shows how a match is built, not only its text.
	if out, err := run("xpath", saved, `//a[@class="t"][1]`, "-html"); err != nil || !strings.Contains(out, `<a class="t" href="/s/one/">One</a>`) {
		t.Errorf("xpath -html: %v\n%s", err, out)
	}

	if _, err := run("Missing", "list"); err == nil || !strings.Contains(err.Error(), "no module file") {
		t.Errorf("a missing module: %v", err)
	}
	if _, err := run("xpath", srv.URL+"/list", "//a[contains(@class,"); err == nil {
		t.Error("a broken expression should be reported, not come back empty")
	}
}
