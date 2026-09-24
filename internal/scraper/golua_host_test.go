package scraper

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

func TestGoluaOpen(t *testing.T) {
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
		m.AddOptionEdit('token', 'Token')
		m.Storage['mode'] = 2
	end
end
GetInfo = shared.GetInfo
function Fail() error('broken on purpose') end`,
	})
	h := &Host{LuaDir: luaDir}
	file := filepath.Join(luaDir, "modules", "Example.lua")

	r, err := h.openGolua(context.Background(), file, "example", "https://two.example")
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

	if _, err := h.openGolua(context.Background(), file, "Nope", ""); err == nil ||
		!strings.Contains(err.Error(), `declares no website named "Nope"`) {
		t.Errorf("unknown site: got %v", err)
	}
}

func TestGoluaUnportedLibraryFailsWhenCalled(t *testing.T) {
	luaDir := writeTree(t, map[string]string{
		"modules/Js.lua": `
local duktape = require 'fmd.duktape'
function Init() local m = NewWebsiteModule(); m.Name = 'Js'; m.OnGetInfo = 'GetInfo' end
function GetInfo() return duktape.ExecJS('1') end`,
	})
	h := &Host{LuaDir: luaDir}
	r, err := h.openGolua(context.Background(), filepath.Join(luaDir, "modules", "Js.lua"), "", "")
	if err != nil {
		t.Fatalf("requiring an unported library should not stop the module loading: %v", err)
	}
	if _, err := r.call("OnGetInfo"); err == nil || !strings.Contains(err.Error(), "fmd.duktape.ExecJS is not ported to golua yet") {
		t.Errorf("got %v, want an error naming fmd.duktape.ExecJS", err)
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

func openLoop(t *testing.T, ctx context.Context) (*goluaRunner, error) {
	t.Helper()
	luaDir := writeTree(t, map[string]string{"modules/Loop.lua": loopModule})
	h := &Host{LuaDir: luaDir}
	return h.openGolua(ctx, filepath.Join(luaDir, "modules", "Loop.lua"), "", "")
}

func TestGoluaCPULimit(t *testing.T) {
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

func TestGoluaCancellation(t *testing.T) {
	// withCancel opens a runner whose Lua can cancel its own context, and
	// poll, which stands in for a binding such as HTTP that checks on entry.
	withCancel := func(t *testing.T) (*goluaRunner, context.Context) {
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		r, err := openLoop(t, ctx)
		if err != nil {
			t.Fatal(err)
		}
		env := r.r.GlobalEnv()
		setGoFunc(r.r, env, "cancel", func(th *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			cancel()
			return c.Next(), nil
		}, 0, false)
		setGoFunc(r.r, env, "poll", func(th *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
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
		if !r.r.GlobalEnv().Get(rt.StringValue("RAN")).IsNil() {
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

// goluaCompileFailures are the upstream files that Lua 5.5 rejects because
// they assign to a for loop's control variable. GroupLe is a template, so the
// eight modules that require it fail with it.
var goluaCompileFailures = []string{
	"AllHentai", "LeerCapitulo", "MintManga", "ReadManga", "RuMIX",
	"SeiManga", "SelfMangaRU", "UsagiOne", "Zazaza",
}

// goluaPendingBindings are modules that call an unported binding from Init.
// Each comes off this list when its binding is ported.
var goluaPendingBindings = []string{
	"FanFox", // fmd.mangafoxwatermark.LoadTemplate
}

// TestGoluaDeclarationsMatchGopher loads every upstream module with both
// runtimes and requires them to declare the same sites, handlers and options.
func TestGoluaDeclarationsMatchGopher(t *testing.T) {
	dir := luaDir(t)
	reg := NewRegistry("", "")
	if err := reg.Use(filepath.Dir(dir), "test"); err != nil {
		t.Fatal(err)
	}
	h := &Host{LuaDir: dir}
	ctx := context.Background()

	var goluaOnly, pending []string
	matched := 0
	for _, m := range reg.Modules() {
		g, gErr := h.Open(ctx, m.File, "", "")
		n, nErr := h.openGolua(ctx, m.File, "", "")
		switch {
		case gErr != nil && nErr != nil:
			continue // fails in both, such as MangaPlus needing the C protobuf library
		case gErr != nil:
			t.Errorf("%s: loads with golua but not gopher-lua: %v", m.Name, gErr)
			continue
		case nErr != nil:
			switch {
			case strings.Contains(nErr.Error(), "constant variable"):
				goluaOnly = append(goluaOnly, m.Name)
			case strings.Contains(nErr.Error(), "is not ported to golua yet"):
				pending = append(pending, m.Name)
			default:
				t.Errorf("%s: loads with gopher-lua but not golua: %v", m.Name, nErr)
			}
			g.Close()
			continue
		}
		if !reflect.DeepEqual(g.Sites(), n.sites) {
			for i := range g.Sites() {
				if i < len(n.sites) && !reflect.DeepEqual(g.Sites()[i], n.sites[i]) {
					t.Errorf("%s: declarations differ\ngopher-lua %+v\n     golua %+v", m.Name, g.Sites()[i], n.sites[i])
				}
			}
			if len(g.Sites()) != len(n.sites) {
				t.Errorf("%s: gopher-lua declares %d sites, golua %d", m.Name, len(g.Sites()), len(n.sites))
			}
		} else {
			matched++
		}
		g.Close()
	}

	sort.Strings(goluaOnly)
	if !reflect.DeepEqual(goluaOnly, goluaCompileFailures) {
		t.Errorf("modules Lua 5.5 rejects:\n got %v\nwant %v", goluaOnly, goluaCompileFailures)
	}
	sort.Strings(pending)
	if !reflect.DeepEqual(pending, goluaPendingBindings) {
		t.Errorf("modules waiting on an unported binding:\n got %v\nwant %v", pending, goluaPendingBindings)
	}
	t.Logf("%d module files declare the same sites in both runtimes", matched)
}
