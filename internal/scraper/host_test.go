package scraper

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// luaDir returns the FMD2 lua tree, skipping the test when none is available.
// The modules are GPL-2.0-only and are never vendored into this repository.
func luaDir(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("ATSUME_FMD2_DIR")
	if dir == "" {
		dir = filepath.Join("..", "txquery", "testdata", "fmd2")
	}
	lua := filepath.Join(dir, "lua")
	if _, err := os.Stat(lua); err != nil {
		t.Skip("no FMD2 checkout; set ATSUME_FMD2_DIR to point at one")
	}
	return lua
}

// madaraSite serves the markup shape that the Madara template parses. Madara
// alone backs 125 upstream modules, so it is the template worth proving.
func madaraSite(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	var base string

	mux.HandleFunc("/wp-admin/admin-ajax.php", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<div class="post-title"><h3><a href="%s/manga/solo-leveling/">Solo Leveling</a></h3></div>
			<div class="post-title"><h3><a href="%s/manga/omniscient/">Omniscient Reader</a></h3></div>
		</body></html>`, base, base)
	})

	mux.HandleFunc("/manga/solo-leveling/", func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "chapter") {
			return
		}
		fmt.Fprintf(w, `<html><body>
			<div class="post-title"><h1>Solo Leveling</h1></div>
			<div class="summary_image"><img data-src="%s/cover.jpg"></div>
			<div class="author-content"><a>Chugong</a></div>
			<div class="artist-content"><a>Jang Sung-Rak</a></div>
			<div class="genres-content"><a>Action</a><a>Fantasy</a></div>
			<div class="summary_content">
				<div class="summary-heading"><h5>Status</h5></div>
				<div class="summary-content">Completed</div>
			</div>
			<div class="summary__content"><p>Ten years ago, the gates appeared.</p></div>
			<li class="wp-manga-chapter"><a href="%s/manga/solo-leveling/chapter-2/">Chapter 2</a></li>
			<li class="wp-manga-chapter"><a href="%s/manga/solo-leveling/chapter-1/">Chapter 1</a></li>
		</body></html>`, base, base, base)
	})

	mux.HandleFunc("/manga/solo-leveling/chapter-1/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, `<html><body>
			<div class="page-break"><img data-src="%s/p1.jpg"></div>
			<div class="page-break"><img data-src="%s/p2.jpg"></div>
			<div class="page-break"><img data-src="%s/p3.jpg"></div>
		</body></html>`, base, base, base)
	})

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)
	return srv
}

// writeModule writes a module in the exact shape upstream Madara modules use:
// Init() fills the table and returns nothing, and the handlers delegate to the
// shared template.
func writeModule(t *testing.T, rootURL string) string {
	t.Helper()
	src := fmt.Sprintf(`
function Init()
	local m = NewWebsiteModule()
	m.ID               = '46e0c618a19748d6af150c2f198f5360'
	m.Name             = 'TestMadara'
	m.RootURL          = '%s'
	m.Category         = 'English'
	m.OnGetNameAndLink = 'GetNameAndLink'
	m.OnGetInfo        = 'GetInfo'
	m.OnGetPageNumber  = 'GetPageNumber'
end

local Template = require 'templates.Madara'

function GetNameAndLink()
	Template.GetNameAndLink()
	return no_error
end

function GetInfo()
	Template.GetInfo()
	return no_error
end

function GetPageNumber()
	return Template.GetPageNumber()
end
`, rootURL)
	path := filepath.Join(t.TempDir(), "testmadara.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestMadaraEndToEnd runs the real upstream Madara template against a local
// site: module load, directory listing, info and chapter pages.
func TestMadaraEndToEnd(t *testing.T) {
	dir := luaDir(t)
	srv := madaraSite(t)

	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), writeModule(t, srv.URL), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if got := r.Module().Name; got != "TestMadara" {
		t.Errorf("module name = %q", got)
	}
	if got := r.Module().ID; got != "46e0c618a19748d6af150c2f198f5360" {
		t.Errorf("module id = %q", got)
	}

	t.Run("GetNameAndLink", func(t *testing.T) {
		entries, err := r.GetNameAndLink(0)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("got %d entries, want 2: %+v", len(entries), entries)
		}
		if entries[0].Name != "Solo Leveling" {
			t.Errorf("entry name = %q", entries[0].Name)
		}
		if !strings.HasSuffix(entries[0].Link, "/manga/solo-leveling/") {
			t.Errorf("entry link = %q", entries[0].Link)
		}
	})

	t.Run("GetInfo", func(t *testing.T) {
		info, err := r.GetInfo(srv.URL + "/manga/solo-leveling/")
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range []struct{ field, got, want string }{
			{"Title", info.Title, "Solo Leveling"},
			{"Authors", info.Authors, "Chugong"},
			{"Artists", info.Artists, "Jang Sung-Rak"},
			{"Genres", info.Genres, "Action, Fantasy"},
			{"Status", info.Status, "completed"},
			{"CoverLink", info.CoverLink, srv.URL + "/cover.jpg"},
		} {
			if c.got != c.want {
				t.Errorf("%s = %q, want %q", c.field, c.got, c.want)
			}
		}
		if !strings.Contains(info.Summary, "the gates appeared") {
			t.Errorf("Summary = %q", info.Summary)
		}
		if n := info.ChapterLinks.Count(); n != 2 {
			t.Fatalf("got %d chapters, want 2", n)
		}
		// The site lists newest first; Madara reverses so that chapters are
		// stored oldest first, which is the order downloads run in.
		if got := info.ChapterNames.Get(0); got != "Chapter 1" {
			t.Errorf("first chapter = %q, want the oldest", got)
		}
		if got := info.ChapterNames.Get(1); got != "Chapter 2" {
			t.Errorf("second chapter = %q", got)
		}
		if !strings.HasSuffix(info.ChapterLinks.Get(0), "/chapter-1/") {
			t.Errorf("chapter links not reversed alongside names: %q", info.ChapterLinks.Get(0))
		}
	})

	t.Run("GetPageNumber", func(t *testing.T) {
		pages, err := r.GetPageNumber(srv.URL + "/manga/solo-leveling/chapter-1/")
		if err != nil {
			t.Fatal(err)
		}
		if len(pages) != 3 {
			t.Fatalf("got %d pages, want 3: %v", len(pages), pages)
		}
		if pages[0] != srv.URL+"/p1.jpg" {
			t.Errorf("first page = %q", pages[0])
		}
	})
}

// TestMadaraChapterProtector exercises the JavaScript branch of the real
// upstream Madara template: a chapter page whose image list is AES-encrypted
// and only recoverable by running the page's own script through fmd.duktape.
//
// The fixture is encrypted with the same crypto-js helpers the template uses to
// decrypt it, so the test covers the whole chain: require() resolution, the
// CommonJS wrapper, the crypto randomness shim, and duk_safe_to_string
// semantics on the result.
func TestMadaraChapterProtector(t *testing.T) {
	dir := luaDir(t)
	rt := newJSRuntime(dir)

	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/manga/protected/chapter-1/", func(w http.ResponseWriter, r *http.Request) {
		const nonce = "b4dc0ffee0ddf00d"
		// WordPress's chapter protector stores the list double-encoded: the
		// ciphertext decrypts to a JSON *string* that itself holds JSON. The
		// template relies on that, so the fixture has to match.
		encrypted, err := rt.Exec(fmt.Sprintf(`
		var CryptoJS = require("utils/crypto-js.min.js");
		var CryptoJSAesJson = require("utils/cryptojs-aes-format.js");
		var listText = JSON.stringify([%q, %q]);
		CryptoJS.AES.encrypt(JSON.stringify(listText), %q,
			{ format: CryptoJSAesJson }).toString();
	`, base+"/p1.jpg", base+"/p2.jpg", nonce))
		if err != nil {
			t.Errorf("building the fixture failed: %v", err)
			return
		}
		fmt.Fprintf(w, `<html><body>
		<script id="chapter-protector-data">
			var chapter_data = '%s';
			var wpmangaprotectornonce = '%s';
		</script>
	</body></html>`, encrypted, nonce)
	})

	srv := httptest.NewServer(mux)
	base = srv.URL
	t.Cleanup(srv.Close)

	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), writeModule(t, srv.URL), "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	pages, err := r.GetPageNumber(srv.URL + "/manga/protected/chapter-1/")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 {
		t.Fatalf("got %d pages, want 2: %v", len(pages), pages)
	}
	if pages[0] != base+"/p1.jpg" || pages[1] != base+"/p2.jpg" {
		t.Errorf("pages = %v", pages)
	}
}

// TestDocumentToString covers HTTP.Document.ToString(), which 153 call sites
// use alongside the 1089 that hand HTTP.Document straight to CreateTXQuery.
func TestDocumentToString(t *testing.T) {
	dir := luaDir(t)
	const body = `<html><body><b>payload</b></body></html>`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, body)
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "doc.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'dddddddddddddddddddddddddddddddd'
	m.Name            = 'DocTest'
	m.RootURL         = '` + srv.URL + `'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	if not HTTP.GET(MODULE.RootURL) then return false end
	-- both forms must work against the same response
	TASK.PageLinks.Add(CreateTXQuery(HTTP.Document).XPathString('//b'))
	TASK.PageLinks.Add(tostring(HTTP.Document.ToString():len()))
	local x = CreateTXQuery()
	x.ParseHTML(HTTP.Document)
	TASK.PageLinks.Add(x.XPathString('//b'))
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

	pages, err := r.GetPageNumber(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 {
		t.Fatalf("got %v", pages)
	}
	if pages[0] != "payload" {
		t.Errorf("CreateTXQuery(HTTP.Document) gave %q", pages[0])
	}
	if want := strconv.Itoa(len(body)); pages[1] != want {
		t.Errorf("HTTP.Document.ToString():len() gave %q, want %s", pages[1], want)
	}
	if pages[2] != "payload" {
		t.Errorf("ParseHTML(HTTP.Document) gave %q", pages[2])
	}
}

// TestCommaText covers the TStringList property FanFox assigns its page list to.
func TestCommaText(t *testing.T) {
	s := NewStrings()
	s.SetCommaText(`a.jpg,b.jpg,"c,with,commas.jpg"`)
	if s.Count() != 3 {
		t.Fatalf("got %d items: %v", s.Count(), s.All())
	}
	if s.Get(2) != "c,with,commas.jpg" {
		t.Errorf("quoted item = %q", s.Get(2))
	}
	if got := s.CommaText(); got != `a.jpg,b.jpg,"c,with,commas.jpg"` {
		t.Errorf("round trip = %q", got)
	}
}

// TestContextNodeArgument covers x.XPathString(expr, node), the two-argument
// form templates use while iterating a node set.
//
// It is pinned because the context argument is easy to drop silently: when it
// is ignored the expression evaluates against the document root, matches
// nothing, and every chapter name comes back empty with no error anywhere.
func TestContextNodeArgument(t *testing.T) {
	dir := luaDir(t)
	path := filepath.Join(t.TempDir(), "ctx.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'ccccccccccccccccccccccccccccc111'
	m.Name            = 'CtxTest'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	local x = CreateTXQuery('<ul><li><a href="/c1"><span class="n">  One  </span></a></li>' ..
	'<li><a href="/c2"><span class="n">Two</span></a></li></ul>')
	for v in x.XPath('//li/a').Get() do
	TASK.PageLinks.Add(v.GetAttribute('href'))
	TASK.PageLinks.Add(x.XPathString('span[@class="n"]/normalize-space(.)', v))
	TASK.PageLinks.Add(v.XPathString('span[@class="n"]/normalize-space(.)'))
	end
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

	got, err := r.GetPageNumber("https://example.invalid/c")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/c1", "One", "One", "/c2", "Two", "Two"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestStatusDefaults pins MangaInfoStatusIfPos against uBaseUnit.pas.
//
// Modules call it with a single argument and depend on the default keyword
// lists; without them a site reporting "Completed" records no status at all.
// The order also matters: upstream tries ongoing before completed.
func TestStatusDefaults(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Completed", "completed"},
		{"Ongoing", "ongoing"},
		{"On Hiatus", "hiatus"},
		{"Cancelled", "dropped"},
		{"", ""},
		{"something else", ""},
	}
	for _, c := range cases {
		got := MangaInfoStatusIfPos(c.in, defaultOngoing, defaultCompleted, defaultHiatus, defaultDropped)
		if got != c.want {
			t.Errorf("MangaInfoStatusIfPos(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Ongoing wins when a status matches both lists, as upstream orders it.
	if got := MangaInfoStatusIfPos("Ongoing (was completed)", "ongoing", "complete", "", ""); got != "ongoing" {
		t.Errorf("overlapping status = %q, want ongoing", got)
	}
}

// TestCookiesRoundTrip covers HTTP.Cookies in both directions.
//
// A module setting a cookie expects it sent — age gates and session tokens are
// set that way — and a module checking one after a login expects to read what
// the server issued. Neither worked while the list was write-only.
func TestCookiesRoundTrip(t *testing.T) {
	dir := luaDir(t)

	var sawCookie string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawCookie = r.Header.Get("Cookie")
		http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123", Path: "/"})
		fmt.Fprint(w, `<html><body><b>ok</b></body></html>`)
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "cookies.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'e5f60718293a4b5c6d7e8f9012345678'
	m.Name            = 'CookieTest'
	m.RootURL         = '` + srv.URL + `'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	HTTP.Cookies.Values['ageGatePass'] = 'True'
	if not HTTP.GET(MODULE.RootURL) then return false end
	TASK.PageLinks.Add('session=' .. HTTP.Cookies.Values['session'])
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

	got, err := r.GetPageNumber(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sawCookie, "ageGatePass=True") {
		t.Errorf("server saw Cookie %q, want it to carry ageGatePass", sawCookie)
	}
	if len(got) != 1 || got[0] != "session=abc123" {
		t.Errorf("module read %v, want the server's session cookie", got)
	}
}

// writeMultiSiteModule writes a file shaped like the twenty-eight upstream
// ones that declare more than one website: two distinct sites, each with its
// own option, and a login on only one of them.
func writeMultiSiteModule(t *testing.T, dir string) string {
	t.Helper()
	const src = `
function Init()
	function AddWebsiteModule(id, name, url)
		local m = NewWebsiteModule()
		m.ID        = id
		m.Name      = name
		m.RootURL   = url
		m.Category  = 'English'
		m.OnGetInfo = 'GetInfo'
		m.AddOptionComboBox('imagesize', 'Image size:', 'Auto\nOriginal', 0)
		return m
	end
	AddWebsiteModule('1111', 'Open Site', 'https://open.example')
	local m = AddWebsiteModule('2222', 'Gated Site', 'https://gated.example')
	m.AccountSupport = true
	m.OnLogin        = 'GatedLogin'
end

function GatedLogin() return true end
function GetInfo() return no_error end
`
	path := filepath.Join(dir, "TwoSites.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestOneFileDeclaresSeveralSites covers the loader's worst assumption.
//
// A file was taken to be a website, so a file declaring several exposed only
// the last and merged every declaration's options into one list. That put
// ExHentai's login behind E-Hentai's name and showed the same dropdown twice.
func TestOneFileDeclaresSeveralSites(t *testing.T) {
	dir := luaDir(t)
	path := writeMultiSiteModule(t, t.TempDir())
	h := &Host{LuaDir: dir}

	// Everything the file declares is available, in declaration order.
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	sites := r.Sites()
	if len(sites) != 2 {
		t.Fatalf("got %d sites, want both", len(sites))
	}
	if sites[0].Name != "Open Site" || sites[1].Name != "Gated Site" {
		t.Errorf("sites = %q, %q", sites[0].Name, sites[1].Name)
	}
	// An unnamed request takes the first, not the last.
	if r.Module().Name != "Open Site" {
		t.Errorf("default site = %q, want the first declared", r.Module().Name)
	}
	// Each declaration keeps its own options. They used to share a slice, so
	// this was two.
	if n := len(r.Module().Options); n != 1 {
		t.Errorf("got %d options, want the one this site declares", n)
	}
	if r.HasHandler("OnLogin") {
		t.Error("the login belongs to the other site in this file")
	}
	r.Close()

	// The second site is reachable, with its own capabilities.
	r, err = h.Open(context.Background(), path, "Gated Site", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Module().RootURL != "https://gated.example" {
		t.Errorf("root = %q", r.Module().RootURL)
	}
	if !r.HasHandler("OnLogin") {
		t.Error("this site does take a login")
	}
	if n := len(r.Module().Options); n != 1 {
		t.Errorf("got %d options, want one", n)
	}

	// Asking for a site the file does not declare is an error, not a silent
	// fallback to whichever one happened to be last.
	if _, err := h.Open(context.Background(), path, "Nowhere", ""); err == nil {
		t.Error("want an error for an undeclared site")
	}
}

// TestMirrorsShareASite covers the other shape: one site declared under
// several addresses, which is not several sites.
func TestMirrorsShareASite(t *testing.T) {
	const src = `
function Init()
	local function AddWebsiteModule(id, url)
	local m = NewWebsiteModule()
	m.ID        = id
	m.Name      = 'Many Doors'
	m.RootURL   = url
	m.OnGetInfo = 'GetInfo'
	end
	AddWebsiteModule('1111', 'https://one.example')
	AddWebsiteModule('2222', 'https://two.example')
end

function GetInfo() return no_error end
`
	path := filepath.Join(t.TempDir(), "ManyDoors.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{LuaDir: luaDir(t)}

	// By name alone, the first address.
	r, err := h.Open(context.Background(), path, "Many Doors", "")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Module().RootURL; got != "https://one.example" {
		t.Errorf("root = %q, want the first mirror", got)
	}
	r.Close()

	// A chosen address is honoured.
	r, err = h.Open(context.Background(), path, "Many Doors", "https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Module().RootURL; got != "https://two.example" {
		t.Errorf("root = %q, want the chosen mirror", got)
	}
	r.Close()

	// An address upstream has dropped falls back rather than failing: these
	// domains disappear constantly, and a followed series must survive it.
	r, err = h.Open(context.Background(), path, "Many Doors", "https://gone.example")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := r.Module().RootURL; got != "https://one.example" {
		t.Errorf("root = %q, want a fallback to the first mirror", got)
	}
}

// TestSleep covers the upstream global twelve modules call.
//
// A site that drip-feeds images over repeated requests returns only a couple
// per request without a pause between them, so a missing sleep does not just
// error — where it is caught, it loses pages.
func TestSleep(t *testing.T) {
	const src = `
function Init()
	local m = NewWebsiteModule()
	m.ID              = '1'
	m.Name            = 'Sleepy'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	sleep(20)
	sleep(0)
	sleep(-5)
	return true
end
`
	path := filepath.Join(t.TempDir(), "Sleepy.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	h := &Host{LuaDir: luaDir(t)}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	start := time.Now()
	if err := r.testCall("OnGetPageNumber"); err != nil {
		t.Fatalf("sleep is a global upstream provides: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Errorf("returned after %v, want it to have waited", elapsed)
	}
}

// TestSleepHonoursCancellation covers the reason a scrape can be stopped.
//
// A cancelled download must not have to wait out a module's idea of a
// reasonable delay before it notices.
func TestSleepHonoursCancellation(t *testing.T) {
	const src = `
function Init()
	local m = NewWebsiteModule()
	m.ID              = '1'
	m.Name            = 'Sleepy'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	sleep(30000)
	return true
end
`
	path := filepath.Join(t.TempDir(), "Sleepy.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	h := &Host{LuaDir: luaDir(t)}
	r, err := h.Open(ctx, path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	if err = r.testCall("OnGetPageNumber"); err == nil {
		t.Error("a cancelled sleep should report why it stopped")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %v for a cancellation", elapsed)
	}
}

// TestLuaDirectoryEndsWithSeparator covers a contract modules depend on by
// concatenation.
//
// Upstream's fmd.LuaDirectory ends with a path separator, and modules write
// fmd.LuaDirectory .. 'extras\\thing'. Without it the path silently does not
// exist, and the module behaves as though the directory were empty — which
// is how the MangaFox templates went unloaded and every page kept its
// watermark.
func TestLuaDirectoryEndsWithSeparator(t *testing.T) {
	const src = `
function Init()
	local m = NewWebsiteModule()
	m.ID      = '1'
	m.Name    = 'Pathy'
	m.RootURL = 'https://example.invalid'
	DIR = require('fmd.env').LuaDirectory
end
`
	path := filepath.Join(t.TempDir(), "Pathy.lua")
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := luaDir(t)
	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	got := r.testGlobal("DIR")
	if want := dir + string(os.PathSeparator); got != want {
		t.Errorf("fmd.LuaDirectory = %q, want %q", got, want)
	}
	// The thing modules actually do with it.
	joined := got + "modules"
	if _, err := os.Stat(joined); err != nil {
		t.Errorf("concatenating onto it does not reach a real path: %v", err)
	}
}
