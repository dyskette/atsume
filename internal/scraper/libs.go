package scraper

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

func hmacSHA256(data, key string) string {
	m := hmac.New(sha256.New, []byte(key))
	m.Write([]byte(data))
	return string(m.Sum(nil))
}

func aesCTR(data, key, iv []byte) (string, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	if len(iv) != block.BlockSize() {
		return "", fmt.Errorf("iv is %d bytes, want %d", len(iv), block.BlockSize())
	}
	out := make([]byte, len(data))
	cipher.NewCTR(block, iv).XORKeyStream(out, data)
	return string(out), nil
}

// goFn is a Go function exposed to Lua, with the number of fixed arguments it
// takes.
type goFn struct {
	nArgs int
	fn    rt.GoFunctionFunc
}

// newLib builds a library table from Go functions.
func newLib(r *rt.Runtime, fns map[string]goFn) *rt.Table {
	t := rt.NewTable()
	for name, f := range fns {
		setGoFunc(r, t, name, f.fn, f.nArgs, false)
	}
	return t
}

// preloadLibs registers the fmd.* libraries so that require finds them.
func preloadLibs(r *rt.Runtime, luaDir string) {
	preload := r.GlobalEnv().Get(rt.StringValue("package")).AsTable().Get(rt.StringValue("preload")).AsTable()
	add := func(name string, build func(r *rt.Runtime) *rt.Table) {
		loader := newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			return c.PushingNext1(t.Runtime, rt.TableValue(build(t.Runtime))), nil
		}, name, 0, true)
		preload.Set(rt.StringValue(name), rt.FunctionValue(loader))
	}
	add("fmd.crypto", cryptoLib)
	add("fmd.env", func(r *rt.Runtime) *rt.Table { return envLib(luaDir) })
	add("fmd.logger", loggerLib)
	add("fmd.fileutil", fileutilLib)
	add("fmd.gzip", gzipLib)

	add("fmd.imagepuzzle", imagePuzzleLib)
	add("fmd.mangafoxwatermark", watermarkLib)
	add("fmd.duktape", func(r *rt.Runtime) *rt.Table { return duktapeLib(r, luaDir) })

	add("fmd.subprocess", func(r *rt.Runtime) *rt.Table {
		return unsupportedLib("fmd.subprocess", "requires external process execution, which atsume does not implement yet")
	})
}

// watermarkLib exposes the MangaFox watermark remover. The template set
// belongs to the library instance rather than the process: a module loads it
// inside Init(), and each scrape runs its own Init().
func watermarkLib(r *rt.Runtime) *rt.Table {
	var held *watermarkTemplates
	return newLib(r, map[string]goFn{
		// LoadTemplate(directory) returns how many templates were read.
		"LoadTemplate": {1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			dir, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			loaded, err := loadWatermarkTemplates(dir)
			if err != nil {
				// Upstream returns zero and carries on; say so, since a silent
				// zero looks exactly like a page that had no watermark.
				slog.Warn("no MangaFox watermark templates", "dir", dir, "err", err)
				return c.PushingNext1(t.Runtime, rt.IntValue(0)), nil
			}
			if len(loaded.items) == 0 {
				slog.Warn("MangaFox watermark template directory is empty", "dir", dir)
			}
			held = loaded
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(loaded.items)))), nil
		}},
		// RemoveWatermark(filename, asPNG) reports whether one was found.
		"RemoveWatermark": {2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			path, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			if held == nil {
				return c.PushingNext1(t.Runtime, rt.BoolValue(false)), nil
			}
			removed, err := held.remove(path, rt.Truth(c.Arg(1)))
			if err != nil {
				return nil, fmt.Errorf("fmd.mangafoxwatermark.RemoveWatermark: %v", err)
			}
			return c.PushingNext1(t.Runtime, rt.BoolValue(removed)), nil
		}},
	})
}

// duktapeLib exposes fmd.duktape, the name modules use for the JavaScript
// engine whichever engine backs it. Each Lua runtime gets its own JavaScript
// runtime, so scrapes running side by side never share JavaScript state.
func duktapeLib(r *rt.Runtime, luaDir string) *rt.Table {
	js := newJSRuntime(luaDir)
	return newLib(r, map[string]goFn{
		"ExecJS": strFnErr(js.Exec),
	})
}

// strFnErr adapts a single-argument string function that can fail; the
// failure becomes a Lua error.
func strFnErr(fn func(string) (string, error)) goFn {
	return goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		out, err := fn(s)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(out)), nil
	}}
}

// unsupportedLib returns a library whose every function raises an error
// naming itself and why it is missing, so the failure is specific rather than
// an "attempt to call a nil value".
func unsupportedLib(lib, why string) *rt.Table {
	meta := rt.NewTable()
	meta.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, _ := c.Arg(1).ToString()
		fn := newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			return nil, fmt.Errorf("%s.%s %s", lib, name, why)
		}, name, 0, true)
		return c.PushingNext1(t.Runtime, rt.FunctionValue(fn)), nil
	}, "__index", 2, false)))
	t := rt.NewTable()
	t.SetMetatable(meta)
	return t
}

// checkString reads argument n as a string, accepting a number the way Lua's
// own string arguments do.
func checkString(c *rt.GoCont, n int) (string, error) {
	s, ok := c.Arg(n).ToString()
	if !ok || c.Arg(n).IsNil() {
		return "", fmt.Errorf("bad argument #%d (string expected, got %s)", n+1, c.Arg(n).TypeName())
	}
	return s, nil
}

// strFn adapts a single-argument string function.
func strFn(fn func(string) string) goFn {
	return goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(fn(s))), nil
	}}
}

// strFn2 adapts a two-argument string function.
func strFn2(fn func(a, b string) string) goFn {
	return goFn{2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		a, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		b, err := checkString(c, 1)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(fn(a, b))), nil
	}}
}

func cryptoLib(r *rt.Runtime) *rt.Table {
	return newLib(r, map[string]goFn{
		// EncodeURLElement percent-encodes every reserved character, which is
		// what modules rely on when splicing a title into a query string.
		"EncodeURLElement": strFn(func(s string) string {
			return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
		}),
		"EncodeURL":  strFn(func(s string) string { return (&url.URL{Path: s}).EscapedPath() }),
		"DecodeURL":  strFn(func(s string) string { v, _ := url.QueryUnescape(s); return v }),
		"HTMLEncode": strFn(html.EscapeString),
		"HTMLDecode": strFn(html.UnescapeString),
		"EncodeBase64": strFn(func(s string) string {
			return base64.StdEncoding.EncodeToString([]byte(s))
		}),
		"DecodeBase64": strFn(func(s string) string {
			// sites are inconsistent about padding, so accept either form
			if v, err := base64.StdEncoding.DecodeString(s); err == nil {
				return string(v)
			}
			v, _ := base64.RawStdEncoding.DecodeString(s)
			return string(v)
		}),
		"HexToStr": strFn(func(s string) string { v, _ := hex.DecodeString(s); return string(v) }),
		"StrToHex": strFn(func(s string) string { return hex.EncodeToString([]byte(s)) }),
		"SHA256": strFn(func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return string(sum[:])
		}),
		"SHA256Hex": strFn(func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		}),
		"HMAC_SHA256": strFn2(hmacSHA256),
		"HMAC_SHA256Hex": strFn2(func(data, key string) string {
			return hex.EncodeToString([]byte(hmacSHA256(data, key)))
		}),
		"AESCTR": {3, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			var args [3]string
			for i := range args {
				s, err := checkString(c, i)
				if err != nil {
					return nil, err
				}
				args[i] = s
			}
			out, err := aesCTR([]byte(args[0]), []byte(args[1]), []byte(args[2]))
			if err != nil {
				return nil, fmt.Errorf("fmd.crypto.AESCTR: %v", err)
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(out)), nil
		}},
	})
}

func envLib(luaDir string) *rt.Table {
	t := rt.NewTable()
	// Modules branch on this to pick a language-specific directory or parser.
	t.Set(rt.StringValue("SelectedLanguage"), rt.StringValue("en"))
	// Upstream ends this with a path separator, and modules concatenate
	// straight onto it: fmd.LuaDirectory .. 'extras\\mangafoxtemplate'.
	t.Set(rt.StringValue("LuaDirectory"), rt.StringValue(luaDir+string(os.PathSeparator)))
	return t
}

func loggerLib(r *rt.Runtime) *rt.Table {
	logAt := func(level slog.Level) goFn {
		return goFn{1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			msg, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			slog.Log(context.Background(), level, "lua module", "msg", msg)
			return c.Next(), nil
		}}
	}
	return newLib(r, map[string]goFn{
		"Send":      logAt(slog.LevelDebug),
		"SendError": logAt(slog.LevelWarn),
	})
}

func fileutilLib(r *rt.Runtime) *rt.Table {
	return newLib(r, map[string]goFn{
		"ExtractFileNameOnly": strFn(func(s string) string {
			s = s[strings.LastIndexAny(s, `/\`)+1:]
			if i := strings.LastIndex(s, "."); i > 0 {
				s = s[:i]
			}
			return s
		}),
	})
}

func gzipLib(r *rt.Runtime) *rt.Table {
	return newLib(r, map[string]goFn{
		"Decompress": {1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			s, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			zr, err := gzip.NewReader(bytes.NewReader([]byte(s)))
			if err != nil {
				return nil, fmt.Errorf("fmd.gzip.Decompress: %v", err)
			}
			defer zr.Close()
			out, err := io.ReadAll(zr)
			if err != nil {
				return nil, fmt.Errorf("fmd.gzip.Decompress: %v", err)
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(string(out))), nil
		}},
	})
}
