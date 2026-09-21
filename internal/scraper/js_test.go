package scraper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestExecJSBasics(t *testing.T) {
	rt := newJSRuntime("")
	cases := []struct{ name, src, want string }{
		{"literal", `'hello';`, "hello"},
		{"arithmetic", `var a = 6; var b = 7; a * b;`, "42"},
		// Modules end scripts with JSON.stringify and parse the text in Lua.
		{"json-stringify", `var m = 1, ch = 2; JSON.stringify({m: m, ch: ch});`, `{"m":1,"ch":2}`},
		{"undefined-is-empty", `var x;x;`, ""},
		// duk_safe_to_string renders null as "null"; only undefined is blanked.
		{"null-renders", `null;`, "null"},
		// An array stringifies through Array.prototype.toString, not as JSON.
		{"array-not-json", `["a","b"];`, "a,b"},
		{"print-available", `print('hi'); 'ok';`, "ok"},
		// The shape acqqcom uses: a script that defines a global, then reads it.
		{"window-shim", `var window = {}; window.nonce = 'abc'; window.nonce;`, "abc"},
		// FanFox packs its page list as a comma-separated string.
		{"packed-list", `var d = ['a.jpg','b.jpg'].join(','); d;`, "a.jpg,b.jpg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := rt.Exec(c.src)
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestExecJSSyntaxError(t *testing.T) {
	if _, err := newJSRuntime("").Exec(`this is not javascript`); err == nil {
		t.Fatal("expected an error")
	}
}

// TestExecJSTimeout covers the case that matters operationally: a scraped page
// containing a script that never terminates must not pin a worker.
func TestExecJSTimeout(t *testing.T) {
	rt := newJSRuntime("")
	rt.timeout = 200 * time.Millisecond

	start := time.Now()
	_, err := rt.Exec(`while (true) {}`)
	if err == nil {
		t.Fatal("expected the interrupt to stop an infinite loop")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("interrupt took %s", elapsed)
	}
	if !strings.Contains(err.Error(), "exceeded") {
		t.Errorf("error = %v, want it to mention the timeout", err)
	}
}

// TestRequireRejectsEscape guards the one capability granted to untrusted
// scripts. The script text comes from a scraped page, so require() must not be
// usable to read arbitrary files.
func TestRequireRejectsEscape(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "utils"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A real file outside the module directory, to prove it stays unreachable.
	secret := filepath.Join(filepath.Dir(dir), "secret.js")
	_ = os.WriteFile(secret, []byte("module.exports = 'leaked';"), 0o644)
	t.Cleanup(func() { os.Remove(secret) })

	rt := newJSRuntime(dir)
	for _, name := range []string{
		"../secret.js",
		"utils/../../secret.js",
		"/etc/passwd",
		"utils/../../../../../../etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := rt.Exec(`require(` + "`" + name + "`" + `);`); err == nil {
				t.Errorf("require(%q) was allowed", name)
			}
		})
	}
}

func TestRequireMissingFile(t *testing.T) {
	rt := newJSRuntime(t.TempDir())
	if _, err := rt.Exec(`require("utils/nope.js");`); err == nil {
		t.Fatal("expected an error for a missing module")
	}
}

// TestRequireCryptoJS exercises the exact chain templates/Madara.lua uses for
// chapter-protector pages: load crypto-js, load the AES JSON formatter that
// reads CryptoJS off the global scope, and round-trip a value through them.
func TestRequireCryptoJS(t *testing.T) {
	dir := luaDir(t)
	if _, err := os.Stat(filepath.Join(dir, "utils", "crypto-js.min.js")); err != nil {
		t.Skip("upstream checkout has no crypto-js helper")
	}
	rt := newJSRuntime(dir)

	const src = `
		var CryptoJS = require("utils/crypto-js.min.js");
		var CryptoJSAesJson = require("utils/cryptojs-aes-format.js");
		var secret = CryptoJS.AES.encrypt(
			JSON.stringify(["p1.jpg", "p2.jpg"]), "passphrase",
			{ format: CryptoJSAesJson }
		).toString();
		JSON.stringify(
			CryptoJS.AES.decrypt(secret, "passphrase", { format: CryptoJSAesJson })
				.toString(CryptoJS.enc.Utf8)
		);
	`
	got, err := rt.Exec(src)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "p1.jpg") || !strings.Contains(got, "p2.jpg") {
		t.Fatalf("round trip produced %q", got)
	}
}

// TestConsoleDoesNotAbort covers cryptojs-aes-format.js, which calls
// console.warn on unusual passphrases. Without a console shim the whole script
// would die on what upstream treats as a warning.
func TestConsoleDoesNotAbort(t *testing.T) {
	got, err := newJSRuntime("").Exec(`console.warn('careful'); console.log('x', 1); 'survived';`)
	if err != nil {
		t.Fatal(err)
	}
	if got != "survived" {
		t.Errorf("got %q", got)
	}
}

// TestDuktapeFromLua checks the binding as a module sees it.
func TestDuktapeFromLua(t *testing.T) {
	dir := luaDir(t)
	path := filepath.Join(t.TempDir(), "jsmod.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'ffffffffffffffffffffffffffffffff'
	m.Name            = 'JSTest'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	local duktape = require 'fmd.duktape'
	local s = duktape.ExecJS('var d = ["a.jpg","b.jpg"].join(","); d;')
	for i in s:gmatch('[^,]+') do
		TASK.PageLinks.Add(i)
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

	pages, err := r.GetPageNumber("https://example.invalid/c/1")
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0] != "a.jpg" || pages[1] != "b.jpg" {
		t.Fatalf("pages = %v", pages)
	}
}

// TestExecJSErrorSurfacesToLua confirms a broken script raises in Lua rather
// than silently yielding an empty page list.
func TestExecJSErrorSurfacesToLua(t *testing.T) {
	dir := luaDir(t)
	path := filepath.Join(t.TempDir(), "jsbad.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID              = 'eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee'
	m.Name            = 'JSBad'
	m.RootURL         = 'https://example.invalid'
	m.OnGetPageNumber = 'GetPageNumber'
end

function GetPageNumber()
	require 'fmd.duktape'.ExecJS('@@ not javascript @@')
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

	if _, err := r.GetPageNumber("https://example.invalid/c/1"); err == nil {
		t.Fatal("expected the JavaScript error to reach the caller")
	}
}
