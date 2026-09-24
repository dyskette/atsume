package scraper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runLua evaluates a chunk in a loaded module and returns what it assigned to
// RESULT.
func runLua(t *testing.T, body string) (string, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Bits.lua")
	src := `
function Init()
	local m = NewWebsiteModule()
	m.ID      = '1'
	m.Name    = 'Bits'
	m.RootURL = 'https://example.invalid'
	RESULT = tostring((function() ` + body + ` end)())
end
`
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{LuaDir: luaDir(t)}
	r, err := h.Open(context.Background(), path, "", "")
	if err != nil {
		return "", err
	}
	defer r.Close()
	return r.testGlobal("RESULT"), nil
}

// TestLua53Operators pins the Lua 5.3 operators the modules use: bitwise
// operators and floor division, which fourteen upstream modules depend on.
// Every expected value was taken from an independent Lua 5.4 implementation
// rather than worked out by hand; note that floor division of a float yields
// a float, as in Lua.
func TestLua53Operators(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{"and", "return 0xF0 & 0x3C", "48"},
		{"or", "return 0xF0 | 0x0F", "255"},
		{"xor", "return 0xFF ~ 0x0F", "240"},
		{"not", "return ~0", "-1"},
		{"not of 255", "return ~255", "-256"},
		{"shift left", "return 1 << 4", "16"},
		{"shift right", "return 256 >> 4", "16"},
		{"shift right is logical, not arithmetic", "return -1 >> 60", "15"},
		{"shifting past the width yields zero", "return 1 << 64", "0"},
		{"a negative count shifts the other way", "return 256 >> -4", "4096"},
		{"floor division", "return 7 // 2", "3"},
		{"floor division rounds down, not toward zero", "return -7 // 2", "-4"},
		{"floor division of floats", "return 7.5 // 2", "3.0"},

		// Precedence, which is where a hand-written rewrite would go wrong:
		// or is loosest, then xor, then and, then the shifts, all tighter
		// than comparison and looser than concatenation.
		{"and binds tighter than or", "return 1 | 2 & 3", "3"},
		{"and binds tighter than xor", "return 1 ~ 2 & 3", "3"},
		{"addition binds tighter than shift", "return 2 + 3 << 1", "10"},
		{"addition binds tighter than shift, other side", "return 1 << 2 + 1", "8"},
		{"unary not binds tighter than and", "return ~0 & 0xFF", "255"},
		{"floor division associates left", "return 6 // 4 * 2", "2"},
		{"a mixture", "return (0xFF & 0x0F) << 2 | 1", "61"},

		// What the modules actually do: a 32-bit shuffle and UTF-8 decoding.
		{
			name: "the shuffle one module runs",
			body: `local s = 123456789
			       for i = 1, 20 do s = (s * 1664525 + 1013904223) & 0xffffffff end
			       return s`,
			want: "3679735209",
		},
		{
			name: "decoding a UTF-8 sequence by hand",
			body: `local b1, b2, b3 = 0xE6, 0x97, 0xA5
			       return ((b1 & 0x0F) << 12) | ((b2 & 0x3F) << 6) | (b3 & 0x3F)`,
			want: "26085",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := runLua(t, c.body)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if got != c.want {
				t.Errorf("got %s, want %s", got, c.want)
			}
		})
	}
}

// TestLua53OperatorsUseRealIntegers covers the 64-bit range. Numbers past
// 2^53, which a float cannot hold exactly, come out right because Lua 5.5 has
// true integers; a fractional operand still fails, as in Lua.
func TestLua53OperatorsUseRealIntegers(t *testing.T) {
	for body, want := range map[string]string{
		"return (1 << 53) + 1 | 1":   "9007199254740993",
		"return 2^60 & 0xFF":         "0",
		"return 1 << 63":             "-9223372036854775808",
		"return 0xFFFFFFFF & 0xFFFF": "65535",
	} {
		got, err := runLua(t, body)
		if err != nil || got != want {
			t.Errorf("%s: got %q, %v; want %s", body, got, err, want)
		}
	}
	if _, err := runLua(t, "return 1.5 & 1"); err == nil ||
		!strings.Contains(err.Error(), "no integer representation") {
		t.Errorf("a fractional operand: got %v, want an error", err)
	}
}
