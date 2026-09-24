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
	lua "github.com/yuin/gopher-lua"
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

func runGopherLibs(t *testing.T, luaDir string, doc []byte, src string) (string, bool) {
	t.Helper()
	L := lua.NewState()
	defer L.Close()
	registerDocument(L)
	registerStrings(L)
	registerValues(L)
	registerImagePuzzle(L)
	registerLibs(L, luaDir)
	L.SetGlobal("DOC", pushDocument(L, &Document{data: doc}))
	if err := L.DoString(src); err != nil {
		return "", false
	}
	return L.Get(-1).String(), true
}

func runGoluaLibs(t *testing.T, luaDir string, doc []byte, src string) (string, bool) {
	t.Helper()
	r := newLuaRuntime(io.Discard, luaDir)
	preloadLibs(r, luaDir)
	r.GlobalEnv().Set(rt.StringValue("DOC"), pushGoluaDocument(r, &Document{data: doc}))
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

// TestGoluaLibsMatchGopher runs the image puzzle, watermark and JavaScript
// libraries through both runtimes and requires the same results.
func TestGoluaLibsMatchGopher(t *testing.T) {
	luaDir := filepath.Join(t.TempDir(), "lua")
	const p = `local p = require('fmd.imagepuzzle').Create(2, 2) `
	snippets := map[string]string{
		"puzzle fields":          p + `return p.HorBlock .. p.VerBlock .. p.Multiply .. #p.Matrix .. p.Matrix[0] .. p.Matrix[3] .. tostring(p.Matrix[4])`,
		"puzzle New alias":       `return tostring(#require('fmd.imagepuzzle').New(3, 2).Matrix)`,
		"puzzle set matrix":      p + `p.Matrix[0] = 3; p.Matrix[3] = 0; return p.Matrix[0] .. p.Matrix[3]`,
		"puzzle set float index": p + `p.Matrix[1.9] = 2; return p.Matrix[1] .. p.Matrix[2]`,
		"puzzle out of range":    p + `p.Matrix[4] = 1`,
		"puzzle negative index":  p + `return tostring(p.Matrix[-1])`,
		"puzzle multiply":        p + `p.Multiply = 2; return tostring(p.Multiply)`,
		"puzzle unknown field":   p + `p.Nope = 1; return tostring(p.Nope)`,
		"descramble in place":    p + `p.Matrix[0] = 3; p.Matrix[3] = 0; p.DeScramble(DOC, DOC); return DOC.Size .. require('fmd.crypto').SHA256Hex(DOC.ToString())`,
		"descramble one stream":  p + `p.DeScramble(DOC)`,
		"descramble not image":   p + `local q = require('fmd.imagepuzzle').Create(1, 1); q.DeScramble(p, p)`,
		"js arithmetic":          `return require('fmd.duktape').ExecJS('1 + 1')`,
		"js modern syntax":       `return require('fmd.duktape').ExecJS('[1, 2, 3].map(x => x * 2).join(",")')`,
		"js string":              `return require('fmd.duktape').ExecJS('"a" + "b"')`,
		"js error":               `return require('fmd.duktape').ExecJS('throw new Error("broken")')`,
		"watermark, no dir":      `local w = require 'fmd.mangafoxwatermark'; return w.LoadTemplate('/nonexistent') .. tostring(w.RemoveWatermark('/tmp/none.png', true))`,
	}
	for name, src := range snippets {
		t.Run(name, func(t *testing.T) {
			gotG, okG := runGopherLibs(t, luaDir, scrambledPNG(t), src)
			gotN, okN := runGoluaLibs(t, luaDir, scrambledPNG(t), src)
			if okG != okN {
				t.Fatalf("gopher-lua ok=%v %q, golua ok=%v %q", okG, gotG, okN, gotN)
			}
			if gotG != gotN {
				t.Errorf("gopher-lua %q\n     golua %q", gotG, gotN)
			}
		})
	}
}
