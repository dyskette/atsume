package scraper

import (
	"io"
	"path/filepath"
	"testing"

	rt "github.com/arnodel/golua/runtime"
	lua "github.com/yuin/gopher-lua"
)

// runtimeForInBug reports whether the Lua runtime miscompiles a generic-for
// header containing a chained call.
//
// This is the single check for a defect that would otherwise show up as 255
// modules mysteriously returning nothing. Tests that depend on the fix consult
// it so the failure is reported once, here, rather than everywhere.
func runtimeForInBug() bool {
	const src = `
local items = {'a','b','c'}
local function makeIter()
  local i = 0
  return function() i = i + 1; return items[i] end
end
local obj = { Get = makeIter }
local function maker() return obj end
local n = 0
for v in maker().Get() do n = n + 1 end
result = n
`
	L := lua.NewState()
	defer L.Close()
	if err := L.DoString(src); err != nil {
		return true
	}
	return lua.LVAsNumber(L.GetGlobal("result")) != 3
}

// TestRuntimeSupportsChainedForIn guards the gopher-lua patch described in
// docs/UPSTREAM.md.
//
// `x.XPath(expr).Get()` is how FMD2 modules iterate a node set — 160 call
// sites across 109 modules and 20 shared templates. Unpatched, every one of
// them raises "attempt to call a non-function object" at handler time, which no
// module load test can detect.
func TestRuntimeSupportsChainedForIn(t *testing.T) {
	t.Run("gopher-lua", func(t *testing.T) {
		if runtimeForInBug() {
			t.Fatal("the Lua runtime miscompiles a chained call in a generic-for header, " +
				"which breaks 255 of 621 upstream modules.\n" +
				"Apply patches/gopher-lua-generic-for.patch to a fork and add the replace " +
				"directive described in docs/UPSTREAM.md.")
		}
	})
	// golua compiles this correctly without a patch.
	t.Run("golua", func(t *testing.T) {
		got, err := goluaResult(t, `
local items = {'a','b','c'}
local function makeIter()
  local i = 0
  return function() i = i + 1; return items[i] end
end
local obj = { Get = makeIter }
local function maker() return obj end
local n = 0
for v in maker().Get() do n = n + 1 end
result = n`)
		if err != nil || got != "3" {
			t.Fatalf("got %q, %v; want 3", got, err)
		}
	})
}

// goluaResult runs src in a golua runtime built for modules and returns the
// global result as text.
func goluaResult(t *testing.T, src string) (string, error) {
	t.Helper()
	r := newLuaRuntime(io.Discard, filepath.Join(t.TempDir(), "lua"))
	chunk, err := r.CompileAndLoadLuaChunk("test", []byte(src), rt.TableValue(r.GlobalEnv()))
	if err != nil {
		return "", err
	}
	if _, err := rt.Call1(r.MainThread(), rt.FunctionValue(chunk)); err != nil {
		return "", err
	}
	s, _ := r.GlobalEnv().Get(rt.StringValue("result")).ToString()
	return s, nil
}

// TestRuntimeLuaBasics pins the other runtime behaviours the host relies on, so
// a runtime swap cannot quietly change them.
func TestRuntimeLuaBasics(t *testing.T) {
	cases := []struct{ name, src, want string }{
		{"string gmatch", `local o = {} for w in ("a,b,c"):gmatch("[^,]+") do o[#o+1] = w end result = table.concat(o, "|")`, "a|b|c"},
		{"string gsub", `result = ("x/y/z"):gsub("/", "-")`, "x-y-z"},
		{"table sort", `local t = {3,1,2} table.sort(t) result = table.concat(t, ",")`, "1,2,3"},
		{"pcall recovers", `local ok = pcall(function() error("boom") end) result = tostring(ok)`, "false"},
		{"varargs", `local function f(...) return select("#", ...) end result = tostring(f(1,2,3))`, "3"},
		// Modules index chapter lists from zero through the host bindings while
		// using one-based Lua tables, so both conventions must behave.
		{"one-based tables", `local t = {"a","b"} result = t[1] .. t[2]`, "ab"},
	}
	for _, c := range cases {
		t.Run(c.name+"/gopher-lua", func(t *testing.T) {
			L := lua.NewState()
			defer L.Close()
			if err := L.DoString(c.src); err != nil {
				t.Fatal(err)
			}
			if got := lua.LVAsString(L.GetGlobal("result")); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
		t.Run(c.name+"/golua", func(t *testing.T) {
			got, err := goluaResult(t, c.src)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
