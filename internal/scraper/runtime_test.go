package scraper

import (
	"io"
	"path/filepath"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// TestRuntimeSupportsChainedForIn guards the idiom modules iterate node
// sets with: `x.XPath(expr).Get()` in a generic-for header, at 160 call sites
// across 109 modules and 20 templates. gopher-lua miscompiled it and needed a
// patch; golua compiles it correctly, and this keeps it so.
func TestRuntimeSupportsChainedForIn(t *testing.T) {
	got, err := luaResult(t, `
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
}

// luaResult runs src in a runtime built for modules and returns the global
// result as text.
func luaResult(t *testing.T, src string) (string, error) {
	t.Helper()
	r := newLuaRuntime(io.Discard, filepath.Join(t.TempDir(), "lua"), nil)
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
		t.Run(c.name, func(t *testing.T) {
			got, err := luaResult(t, c.src)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
