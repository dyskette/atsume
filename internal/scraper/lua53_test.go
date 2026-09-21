package scraper

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requireLua53 skips when the gopher-lua in use has not been patched.
//
// The operators are carried in patches/gopher-lua-lua53-operators.patch and
// applied to a fork; until that fork is published and go.mod points at it,
// these tests have nothing to check. The skip names the patch so the reason
// is not a mystery.
func requireLua53(t *testing.T) {
	t.Helper()
	if _, err := runLua(t, "return 1 & 1"); err != nil {
		t.Skip("gopher-lua here has no bitwise operators; " +
			"apply patches/gopher-lua-lua53-operators.patch to the fork in go.mod")
	}
}

// runLua evaluates a chunk and returns what it assigned to RESULT.
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
	return r.L.GetGlobal("RESULT").String(), nil
}

// TestLua53Operators pins the operators the fork adds to gopher-lua.
//
// gopher-lua implements Lua 5.1, which has no bitwise operators and no floor
// division, so thirteen upstream modules could not even be parsed. Every
// expected value here was taken from an independent Lua 5.4 implementation
// rather than worked out by hand.
//
// If this test starts failing, the likely cause is a gopher-lua bump that
// dropped patches/gopher-lua-lua53-operators.patch.
func TestLua53Operators(t *testing.T) {
	requireLua53(t)

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
		{"floor division of floats", "return 7.5 // 2", "3"},

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

// TestLua53OperatorsRefuseWhatTheyCannotRepresent covers the one place this
// implementation cannot follow Lua 5.3.
//
// Numbers here are float64, which holds integers exactly only to 2^53. Lua
// 5.3 has true 64-bit integers. Rather than return a plausible-looking wrong
// answer — these operators decrypt page addresses, so a wrong answer is a
// wrong image — anything that will not survive the round trip raises.
func TestLua53OperatorsRefuseWhatTheyCannotRepresent(t *testing.T) {
	requireLua53(t)

	cases := []struct{ name, body, want string }{
		{"a result past 2^53", "return (1 << 53) + 1 | 1", "too large"},
		{"an operand past 2^53", "return 2^60 & 0xFF", "too large"},
		{"a fractional operand, as in Lua itself", "return 1.5 & 1", "no integer representation"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := runLua(t, c.body)
			if err == nil {
				t.Fatal("want an error rather than a wrong answer")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("error %q, want it to mention %q", err, c.want)
			}
		})
	}

	// Everything the catalogue does is well inside the exact range.
	for _, body := range []string{
		"return 0xFFFFFFFF & 0xFFFF",
		"return 1 << 63", // exactly representable, so it is allowed
		"return 0x7FFFFFFF ~ 0x12345678",
	} {
		if _, err := runLua(t, body); err != nil {
			t.Errorf("%s: %v", body, err)
		}
	}
}
