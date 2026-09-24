package scraper

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// evalStd runs a chunk in a runtime from newLuaRuntime and returns its result
// as a string.
func evalStd(t *testing.T, root, src string) (string, error) {
	t.Helper()
	r := newLuaRuntime(io.Discard, root)
	chunk, err := r.CompileAndLoadLuaChunk("test", []byte(src), rt.TableValue(r.GlobalEnv()))
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	v, err := rt.Call1(r.MainThread(), rt.FunctionValue(chunk))
	if err != nil {
		return "", err
	}
	s, _ := v.ToString()
	return s, nil
}

func mustEvalStd(t *testing.T, root, src string) string {
	t.Helper()
	got, err := evalStd(t, root, src)
	if err != nil {
		t.Fatalf("%s: %v", src, err)
	}
	return got
}

func TestLuaRuntimeLibraries(t *testing.T) {
	root := t.TempDir()

	// Everything the FMD2 modules call must be there.
	for _, name := range []string{
		"string.format", "string.gsub", "table.concat", "table.unpack", "math.floor",
		"utf8.char", "os.time", "os.date", "os.clock", "os.remove", "os.rename",
		"io.open", "io.lines", "require", "package.path", "pcall", "tonumber",
		"coroutine.wrap", "load",
	} {
		if got := mustEvalStd(t, root, "return type("+name+")"); got == "nil" {
			t.Errorf("%s is missing", name)
		}
	}

	// And nothing that reaches outside the runtime.
	for _, name := range []string{
		"golib", "runtime", "debug", "dofile", "loadfile",
		"os.execute", "os.exit", "os.getenv", "os.tmpname", "os.setlocale",
		"io.popen", "io.write", "io.output", "io.tmpfile",
	} {
		if got := mustEvalStd(t, root, "return type("+name+")"); got != "nil" {
			t.Errorf("%s should not be available, got a %s", name, got)
		}
	}
}

func TestLuaRuntimeReadsInsideRoot(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "userdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "userdata", "map.txt"), []byte("1;a\r\n2;b\n3;c"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct{ src, want string }{
		"read all, 5.1 spelling": {
			`local f = io.open('userdata/map.txt', 'rb'); local s = f:read('*all'); f:close(); return #s`, "12"},
		"read lines, CRLF dropped": {
			`local f = io.open('userdata/map.txt'); local a, b = f:read('l', 'L'); return a .. '|' .. b`, "1;a|2;b\n"},
		"io.lines, as MangaDex reads its mapping": {
			`local t = {}; for l in io.lines('userdata/map.txt') do t[#t+1] = l end; return table.concat(t, ',')`, "1;a,2;b,3;c"},
		"file:lines": {
			`local n = 0; for _ in io.open('userdata/map.txt'):lines() do n = n + 1 end; return n`, "3"},
		"absolute path inside root": {
			`return io.open(` + luaQuote(filepath.Join(root, "userdata", "map.txt")) + `):read('a'):sub(1, 3)`, "1;a"},
		"missing file is nil and a message": {
			`local f, err = io.open('userdata/none.txt'); return tostring(f) .. ' ' .. err`, "nil userdata/none.txt: No such file or directory"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			if got := mustEvalStd(t, root, c.src); got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}

	if _, err := evalStd(t, root, `local f = io.open('userdata/map.txt'); f:close(); return f:read('a')`); err == nil {
		t.Error("reading a closed file should fail")
	}
}

func TestLuaRuntimeCannotLeaveRoot(t *testing.T) {
	outside := t.TempDir()
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "keep.txt"), []byte("keep"), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{
		secret,
		"../" + filepath.Base(outside) + "/secret.txt",
		"escape/secret.txt",
		"/etc/passwd",
	} {
		src := `local f, err = io.open(` + luaQuote(path) + `); return tostring(f) .. ' ' .. tostring(err)`
		if got := mustEvalStd(t, root, src); !strings.HasPrefix(got, "nil ") {
			t.Errorf("io.open(%q) should fail, got %q", path, got)
		}
		if _, err := evalStd(t, root, `for l in io.lines(`+luaQuote(path)+`) do end`); err == nil {
			t.Errorf("io.lines(%q) should fail", path)
		}
	}

	// Writing, removing and renaming fail the way a read-only filesystem
	// would, and leave the file alone.
	for _, src := range []string{
		`local f, err = io.open('keep.txt', 'w'); return tostring(f) .. ' ' .. err`,
		`local f, err = io.open('new.txt', 'a'); return tostring(f) .. ' ' .. err`,
		`local f, err = io.open('keep.txt', 'r+'); return tostring(f) .. ' ' .. err`,
		`local ok, err = os.remove('keep.txt'); return tostring(ok) .. ' ' .. err`,
		`local ok, err = os.rename('keep.txt', 'moved.txt'); return tostring(ok) .. ' ' .. err`,
	} {
		if got := mustEvalStd(t, root, src); !strings.HasPrefix(got, "nil ") {
			t.Errorf("%s: got %q, want a nil result", src, got)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "keep.txt")); err != nil || string(b) != "keep" {
		t.Errorf("keep.txt changed: %q, %v", b, err)
	}
	for _, name := range []string{"new.txt", "moved.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("%s was created", name)
		}
	}
}

// luaQuote writes s as a Lua long string, so paths need no escaping.
func luaQuote(s string) string { return "[==[" + s + "]==]" }
