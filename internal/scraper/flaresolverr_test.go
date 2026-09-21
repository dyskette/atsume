package scraper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLooksLikeChallenge(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{"cloudflare interstitial", 403, `<title>Just a moment...</title>`, true},
		{"ddos-guard", 503, `<div>DDoS-Guard</div>`, true},
		{"challenge token", 403, `window._cf_chl_opt = {}`, true},
		// An ordinary refusal must not cost a browser run.
		{"plain forbidden", 403, `<h1>Forbidden</h1>`, false},
		{"not found", 404, `Just a moment`, false},
		{"success", 200, `Just a moment`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := looksLikeChallenge(c.status, []byte(c.body)); got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// TestFlaresolverrRecovers covers the whole path: a site refuses with an
// interstitial, the solver returns the real page, and the module sees it.
func TestFlaresolverrRecovers(t *testing.T) {
	dir := luaDir(t)

	var solved int
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `<html><head><title>Just a moment...</title></head><body>
			<div class="cf-browser-verification"></div></body></html>`)
	}))
	t.Cleanup(site.Close)

	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		solved++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"status":"ok","solution":{
			"url":%q,"status":200,
			"response":"<html><body><b>real content</b></body></html>",
			"userAgent":"SolvedAgent/1.0",
			"cookies":[{"name":"cf_clearance","value":"token123"}]
		}}`, site.URL)
	}))
	t.Cleanup(solver.Close)

	path := filepath.Join(t.TempDir(), "cf.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'f60718293a4b5c6d7e8f901234567890'
	m.Name            = 'CFTest'
	m.RootURL         = '` + site.URL + `'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	if not HTTP.GET(MODULE.RootURL) then return false end
	TASK.PageLinks.Add(CreateTXQuery(HTTP.Document).XPathString('//b'))
	TASK.PageLinks.Add('clearance=' .. HTTP.Cookies.Values['cf_clearance'])
	return true
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{LuaDir: dir, Solver: NewFlaresolverr(solver.URL)}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got, err := r.GetPageNumber(site.URL)
	if err != nil {
		t.Fatal(err)
	}
	if solved != 1 {
		t.Errorf("solver called %d times, want 1", solved)
	}
	if len(got) != 2 || got[0] != "real content" {
		t.Fatalf("got %v, want the solved page", got)
	}
	// The clearance cookie must carry forward, or the next request is
	// challenged all over again.
	if got[1] != "clearance=token123" {
		t.Errorf("cookie = %q, want the clearance token", got[1])
	}
}

// TestFlaresolverrNotUsedForOrdinaryRefusal guards against spending a browser
// run on a plain 403.
func TestFlaresolverrNotUsedForOrdinaryRefusal(t *testing.T) {
	dir := luaDir(t)

	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	t.Cleanup(site.Close)

	var solved int
	solver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		solved++
		fmt.Fprint(w, `{"status":"error","message":"should not be called"}`)
	}))
	t.Cleanup(solver.Close)

	path := filepath.Join(t.TempDir(), "plain.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = '0718293a4b5c6d7e8f90123456789012'
	m.Name            = 'PlainTest'
	m.RootURL         = '` + site.URL + `'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	if not HTTP.GET(MODULE.RootURL) then return false end
	return true
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{LuaDir: dir, Solver: NewFlaresolverr(solver.URL)}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if _, err := r.GetPageNumber(site.URL); err == nil {
		t.Error("expected the refusal to surface")
	}
	if solved != 0 {
		t.Errorf("solver was called %d times for a plain 403", solved)
	}
}

// TestAfterImageSaved covers the post-processing hook editing a page in place.
func TestAfterImageSaved(t *testing.T) {
	dir := luaDir(t)
	path := filepath.Join(t.TempDir(), "post.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID                 = '18293a4b5c6d7e8f9012345678901234'
	m.Name               = 'PostTest'
	m.RootURL            = 'https://example.invalid'
	m.OnGetPageNumber    = 'GetPageNumber'
	m.OnAfterImageSaved  = 'AfterImageSaved'
end

function GetPageNumber() return true end

function AfterImageSaved()
	local f = io.open(FILENAME, 'wb')
	f:write('EDITED')
	f:close()
	return true
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if !r.HasHandler("OnAfterImageSaved") {
		t.Fatal("handler should be declared")
	}
	img := filepath.Join(t.TempDir(), "0001.jpg")
	if err := os.WriteFile(img, []byte("ORIGINAL"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := r.AfterImageSaved(img); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(img)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "EDITED") {
		t.Errorf("file = %q, want the handler's edit", out)
	}
}
