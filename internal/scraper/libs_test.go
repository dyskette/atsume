package scraper

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"io"
	"path/filepath"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

// scrambledPNG is an 8×8 image in four 4×4 tiles of different colours, the
// kind of input DeScramble rearranges.
func scrambledPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	colours := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}
	for y := 0; y < 8; y++ {
		for x := 0; x < 8; x++ {
			img.Set(x, y, colours[(y/4)*2+x/4])
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runLibs(t *testing.T, luaDir string, doc []byte, src string) (string, bool) {
	t.Helper()
	r := newLuaRuntime(io.Discard, luaDir, nil)
	preloadLibs(r, luaDir, nil)
	r.GlobalEnv().Set(rt.StringValue("DOC"), pushDocument(r, &Document{data: doc}))
	chunk, err := r.CompileAndLoadLuaChunk("test", []byte(src), rt.TableValue(r.GlobalEnv()))
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	v, err := rt.Call1(r.MainThread(), rt.FunctionValue(chunk))
	if err != nil {
		return "", false
	}
	s, _ := v.ToString()
	return s, true
}

const libsSnippetsPrefix = `local p = require('fmd.imagepuzzle').Create(2, 2) `

// libsSnippets exercise the image puzzle, watermark and JavaScript
// libraries; libsWant holds what each must produce.
var libsSnippets = map[string]string{
	"puzzle fields":           libsSnippetsPrefix + `return p.HorBlock .. p.VerBlock .. p.Multiply .. #p.Matrix .. p.Matrix[0] .. p.Matrix[3] .. tostring(p.Matrix[4])`,
	"puzzle New alias":        `return tostring(#require('fmd.imagepuzzle').New(3, 2).Matrix)`,
	"puzzle set matrix":       libsSnippetsPrefix + `p.Matrix[0] = 3; p.Matrix[3] = 0; return p.Matrix[0] .. p.Matrix[3]`,
	"puzzle set float index":  libsSnippetsPrefix + `p.Matrix[1.9] = 2; return p.Matrix[1] .. p.Matrix[2]`,
	"puzzle out of range":     libsSnippetsPrefix + `p.Matrix[4] = 1`,
	"puzzle negative index":   libsSnippetsPrefix + `return tostring(p.Matrix[-1])`,
	"puzzle multiply":         libsSnippetsPrefix + `p.Multiply = 2; return tostring(p.Multiply)`,
	"puzzle unknown field":    libsSnippetsPrefix + `p.Nope = 1; return tostring(p.Nope)`,
	"descramble in place":     libsSnippetsPrefix + `p.Matrix[0] = 3; p.Matrix[3] = 0; p.DeScramble(DOC, DOC); return DOC.Size .. require('fmd.crypto').SHA256Hex(DOC.ToString())`,
	"descramble one stream":   libsSnippetsPrefix + `p.DeScramble(DOC)`,
	"descramble not image":    libsSnippetsPrefix + `local q = require('fmd.imagepuzzle').Create(1, 1); q.DeScramble(p, p)`,
	"js arithmetic":           `return require('fmd.duktape').ExecJS('1 + 1')`,
	"js modern syntax":        `return require('fmd.duktape').ExecJS('[1, 2, 3].map(x => x * 2).join(",")')`,
	"js string":               `return require('fmd.duktape').ExecJS('"a" + "b"')`,
	"js error":                `return require('fmd.duktape').ExecJS('throw new Error("broken")')`,
	"watermark, no dir":       `return require('fmd.mangafoxwatermark').LoadTemplate('/nonexistent')`,
	"watermark, not the page": `require('fmd.mangafoxwatermark').RemoveWatermark('/tmp/none.png', true)`,
}

// libsWant is what each libsSnippets entry must return, or whether it must
// fail. The values were taken from the gopher-lua bindings these replaced,
// so modules see no difference.
var libsWant = map[string]struct {
	want  string
	fails bool
}{
	"descramble in place":     {"9739f5e578136aad7cbb9e7a70b52f34022e3798334d164ff470a495a21eeda407", false},
	"descramble not image":    {"", true},
	"descramble one stream":   {"", true},
	"js arithmetic":           {"2", false},
	"js error":                {"", true},
	"js modern syntax":        {"2,4,6", false},
	"js string":               {"ab", false},
	"puzzle New alias":        {"6", false},
	"puzzle fields":           {"221403nil", false},
	"puzzle multiply":         {"2", false},
	"puzzle negative index":   {"nil", false},
	"puzzle out of range":     {"", true},
	"puzzle set float index":  {"22", false},
	"puzzle set matrix":       {"30", false},
	"puzzle unknown field":    {"nil", false},
	"watermark, no dir":       {"0", false},
	"watermark, not the page": {"", true},
}

func TestLibs(t *testing.T) {
	luaDir := filepath.Join(t.TempDir(), "lua")
	for name, src := range libsSnippets {
		t.Run(name, func(t *testing.T) {
			w := libsWant[name]
			got, ok := runLibs(t, luaDir, scrambledPNG(t), src)
			if ok == w.fails {
				t.Fatalf("ok=%v %q, want it to fail=%v", ok, got, w.fails)
			}
			if got != w.want {
				t.Errorf("got %q, want %q", got, w.want)
			}
		})
	}
}
