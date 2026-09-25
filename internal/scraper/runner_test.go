package scraper

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// writeTree writes files under a fresh FMD2-shaped checkout and returns its
// lua directory.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for name, body := range files {
		path := filepath.Join(root, "lua", name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return filepath.Join(root, "lua")
}

func TestOpen(t *testing.T) {
	const bom = "\xEF\xBB\xBF"
	luaDir := writeTree(t, map[string]string{
		"templates/Shared.lua": `
local _M = {}
function _M.GetInfo() return 'template:' .. require('fmd.crypto').EncodeURLElement('a b') end
return _M`,
		// Starts with a byte-order mark, as 17 upstream modules do.
		"modules/Example.lua": bom + `
local shared = require 'templates.Shared'
function Init()
	for _, root in ipairs({'https://one.example', 'https://two.example'}) do
		local m = NewWebsiteModule()
		m.ID       = 'e1'
		m.Name     = 'Example'
		m.RootURL  = root
		m.Category = 'English'
		m.OnGetInfo = 'GetInfo'
		m.OnGetPageNumber = 'Missing'
		m.TotalDirectory = '3'
		m.AccountSupport = 1
		m.AddOptionCheckBox('paid', 'Show paid chapters', false)
		m.AddOptionSpinEdit('delay', 'Delay', 1.0)
		m.AddOptionComboBox('lang', 'Language', {'en', 'es'}, 1)
		m.AddOptionComboBox('svr', 'Server', 'Main\nSecondary\r\nCompress', 0)
		m.AddOptionEdit('token', 'Token')
		m.Storage['mode'] = 2
	end
end
GetInfo = shared.GetInfo
function Fail() error('broken on purpose') end`,
	})
	h := &Host{LuaDir: luaDir}
	file := filepath.Join(luaDir, "modules", "Example.lua")

	r, err := h.Open(context.Background(), file, "example", "https://two.example")
	if err != nil {
		t.Fatal(err)
	}
	if len(r.sites) != 2 {
		t.Fatalf("got %d sites, want 2", len(r.sites))
	}
	if r.mod.RootURL != "https://two.example" {
		t.Errorf("selected %s, want the mirror that was asked for", r.mod.RootURL)
	}

	want := &Module{
		ID: "e1", Name: "Example", RootURL: "https://two.example", Category: "English",
		Handlers:       map[string]string{"OnGetInfo": "GetInfo", "OnGetPageNumber": "Missing"},
		AccountSupport: true, TotalDirectory: 3, File: file,
		Options: []Option{
			{Kind: OptionCheckBox, Name: "paid", Caption: "Show paid chapters", Default: false},
			{Kind: OptionSpinEdit, Name: "delay", Caption: "Delay", Default: int64(1)},
			{Kind: OptionComboBox, Name: "lang", Caption: "Language", Items: []string{"en", "es"}, Default: int64(1)},
			{Kind: OptionComboBox, Name: "svr", Caption: "Server", Items: []string{"Main", "Secondary", "Compress"}, Default: int64(0)},
			{Kind: OptionEditBox, Name: "token", Caption: "Token", Default: nil},
		},
	}
	if !reflect.DeepEqual(r.mod, want) {
		t.Errorf("module:\n got %+v\nwant %+v", r.mod, want)
	}
	if r.storage["mode"] != "2" {
		t.Errorf("Storage['mode'] = %q, want \"2\"", r.storage["mode"])
	}

	// A handler reaches a template through require and a library through
	// package.preload.
	v, err := r.call("OnGetInfo")
	if err != nil {
		t.Fatal(err)
	}
	if s, _ := v.ToString(); s != "template:a%20b" {
		t.Errorf("OnGetInfo returned %q", s)
	}

	for event, wantErr := range map[string]string{
		"OnLogin":         "module Example has no OnLogin handler",
		"OnGetPageNumber": `module Example declares OnGetPageNumber="Missing" but defines no such function`,
	} {
		if _, err := r.call(event); err == nil || err.Error() != wantErr {
			t.Errorf("%s: got %v, want %q", event, err, wantErr)
		}
	}
	r.mod.Handlers["OnCheckSite"] = "Fail"
	if _, err := r.call("OnCheckSite"); err == nil || !strings.Contains(err.Error(), "Example/Fail: ") ||
		!strings.Contains(err.Error(), "broken on purpose") {
		t.Errorf("a failing handler should name itself and keep the message, got %v", err)
	}

	if _, err := h.Open(context.Background(), file, "Nope", ""); err == nil ||
		!strings.Contains(err.Error(), `declares no website named "Nope"`) {
		t.Errorf("unknown site: got %v", err)
	}
}

// loopModule declares handlers that loop forever, and a harmless one.
const loopModule = `
function Init()
	local m = NewWebsiteModule()
	m.Name = 'Loop'
	m.OnGetInfo = 'Spin'
	m.OnGetPageNumber = 'Poll'
	m.OnCheckSite = 'Fine'
	m.OnLogin = 'Swallow'
	m.OnGetImageURL = 'Stubborn'
end
function Spin() while true do end end
function Poll() cancel(); while true do poll() end end
function Swallow() cancel(); pcall(poll); return 'looks fine' end
function Stubborn() cancel(); while true do pcall(poll) end end
function Fine() RAN = true; return 'fine' end`

func openLoop(t *testing.T, ctx context.Context) (*Runner, error) {
	t.Helper()
	luaDir := writeTree(t, map[string]string{"modules/Loop.lua": loopModule})
	h := &Host{LuaDir: luaDir}
	return h.Open(ctx, filepath.Join(luaDir, "modules", "Loop.lua"), "", "")
}

func TestCPULimit(t *testing.T) {
	r, err := openLoop(t, context.Background())
	if err != nil {
		t.Fatal(err)
	}
	r.cpuLimit = 1_000_000
	_, err = r.call("OnGetInfo")
	if err == nil || !strings.Contains(err.Error(), "CPU limit") || !strings.Contains(err.Error(), "ran too long") {
		t.Fatalf("a runaway loop should hit the CPU limit, got %v", err)
	}
	// The runner is still usable: the limit applies per call.
	if v, err := r.call("OnCheckSite"); err != nil || v.AsString() != "fine" {
		t.Errorf("after a killed call: %v, %v", v, err)
	}
}

func TestCancellation(t *testing.T) {
	// withCancel opens a runner whose Lua can cancel its own context, and
	// poll, which stands in for a binding such as HTTP that checks on entry.
	withCancel := func(t *testing.T) (*Runner, context.Context) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		r, err := openLoop(t, ctx)
		if err != nil {
			t.Fatal(err)
		}
		env := r.lua.GlobalEnv()
		setGoFunc(r.lua, env, "cancel", func(th *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			cancel()
			return c.Next(), nil
		}, 0, false)
		setGoFunc(r.lua, env, "poll", func(th *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			r.checkContext(th)
			return c.Next(), nil
		}, 0, false)
		return r, ctx
	}

	t.Run("the next binding ends the call", func(t *testing.T) {
		r, ctx := withCancel(t)
		if _, err := r.call("OnGetPageNumber"); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v, want context.Canceled", err)
		}
		// Once cancelled, nothing else runs, and nothing new opens.
		if _, err := r.call("OnCheckSite"); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
		if !r.lua.GlobalEnv().Get(rt.StringValue("RAN")).IsNil() {
			t.Error("a handler ran after cancellation")
		}
		if _, err := openLoop(t, ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("opening with a cancelled context: got %v, want context.Canceled", err)
		}
	})

	t.Run("a result after a caught termination is not taken", func(t *testing.T) {
		r, _ := withCancel(t)
		if v, err := r.call("OnLogin"); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, %v; want context.Canceled", v, err)
		}
	})

	t.Run("a loop that keeps catching it stops at the CPU limit", func(t *testing.T) {
		r, _ := withCancel(t)
		r.cpuLimit = 1_000_000
		if _, err := r.call("OnGetImageURL"); !errors.Is(err, context.Canceled) {
			t.Errorf("got %v, want context.Canceled", err)
		}
	})
}

// cpuHeadroom is how many times the heaviest real handler call luaCPULimit
// must allow, and jsonHeadroom how many times a 1 MB pure-Lua JSON decode.
// Below either, a slow but legitimate handler could be killed.
const (
	cpuHeadroom  = 100
	jsonHeadroom = 10
)

// TestCPULimitHeadroom runs every golden and recorded case,
// the closest thing to real scrapes the suite has, plus the heaviest thing a
// module commonly does in Lua itself: decoding a large JSON response with
// upstream's utils/json. The limit must leave room above both.
func TestCPULimitHeadroom(t *testing.T) {
	dir := luaDir(t)
	var runners []*Runner
	for _, c := range goldenCases {
		t.Run("golden/"+c.name, func(t *testing.T) { runners = append(runners, runGoldenCase(t, dir, c)) })
	}
	root := filepath.Join("testdata", "recorded")
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			t.Run("recorded/"+e.Name(), func(t *testing.T) {
				runners = append(runners, runRecordedCase(t, root, dir, e.Name(), false))
			})
		}
	}
	var peak uint64
	for _, r := range runners {
		peak = max(peak, r.peakCPU)
	}
	t.Logf("%d runners; heaviest call used %d ticks, %.0fx below the limit",
		len(runners), peak, float64(luaCPULimit)/float64(max(peak, 1)))
	if peak*cpuHeadroom > luaCPULimit {
		t.Errorf("the limit is less than %dx the heaviest handler call", cpuHeadroom)
	}

	// About 1 MB of JSON shaped like a chapter list API response.
	var b strings.Builder
	b.WriteString(`{"data":[`)
	for i := range 8000 {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"title":"Chapter %d: A reasonably long chapter title","slug":"chapter-%d","group":{"id":7,"name":"Scans"}}`, i, i, i)
	}
	b.WriteString(`]}`)
	mod := writeTree(t, map[string]string{"modules/Json.lua": `
function Init() local m = NewWebsiteModule(); m.Name = 'Json'; m.OnGetInfo = 'GetInfo' end
function GetInfo() return #require('utils.json').decode(DATA).data end`})
	h := &Host{LuaDir: dir}
	r, err := h.Open(context.Background(), filepath.Join(mod, "modules", "Json.lua"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	r.setGlobal("DATA", rt.StringValue(b.String()))
	if v, err := r.call("OnGetInfo"); err != nil || v.AsInt() != 8000 {
		t.Fatalf("decoding: %v, %v", v, err)
	}
	t.Logf("decoding %d bytes of JSON used %d ticks, %.0fx below the limit",
		b.Len(), r.peakCPU, float64(luaCPULimit)/float64(r.peakCPU))
	if r.peakCPU*jsonHeadroom > luaCPULimit {
		t.Errorf("the limit is less than %dx a 1 MB JSON decode", jsonHeadroom)
	}
}

// lua55Rejects are the upstream modules Lua 5.5 rejects because they assign to
// a for loop's control variable. GroupLe is a template, so the eight modules
// that require it fail with it. TestLoadAllModules pins the list; it shrinks
// when FMD2 fixes the two files.
var lua55Rejects = []string{
	"AllHentai", "LeerCapitulo", "MintManga", "ReadManga", "RuMIX",
	"SeiManga", "SelfMangaRU", "UsagiOne", "Zazaza",
}

// TestHTTPStopsOnCancel checks the HTTP binding's side of cancellation:
// a handler that keeps requesting pages ends at its next request once the
// context is done, instead of looping over failed requests.
func TestHTTPStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	requests := 0
	luaDir := writeTree(t, map[string]string{"modules/Pager.lua": `
function Init()
	local m = NewWebsiteModule()
	m.Name = 'Pager'
	m.OnGetNameAndLink = 'List'
end
function List()
	while true do
		if HTTP.GET('https://pager.example/page/' .. URL) then LINKS.Add(URL) end
		URL = URL + 1
	end
end`})
	h := &Host{LuaDir: luaDir, Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		if requests == 3 {
			cancel()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("ok")), Request: r}, nil
	})}
	r, err := h.Open(ctx, filepath.Join(luaDir, "modules", "Pager.lua"), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.GetNameAndLink(0); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v, want context.Canceled", err)
	}
	if requests != 3 {
		t.Errorf("made %d requests; the one after cancellation should not have been sent", requests)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// testGlobal reads a global as text, and testCall runs an event's handler and
// reports only whether it failed; tests use them to look inside a runner.
func (r *Runner) testGlobal(name string) string {
	s, _ := r.lua.GlobalEnv().Get(rt.StringValue(name)).ToString()
	return s
}

func (r *Runner) testCall(event string) error {
	_, err := r.call(event)
	return err
}
