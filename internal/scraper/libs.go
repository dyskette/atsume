package scraper

import (
	"bytes"
	"compress/gzip"
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

	lua "github.com/yuin/gopher-lua"
)

// registerLibs preloads the fmd.* libraries that FMD2 injects from Pascal. The
// pure-Lua utils.* modules need no help: they resolve through package.path
// against the upstream checkout.
func registerLibs(L *lua.LState, luaDir string) {
	preload := func(name string, fn lua.LGFunction) {
		L.PreloadModule(name, fn)
	}
	preload("fmd.crypto", cryptoLoader)
	preload("fmd.env", envLoader(luaDir))
	preload("fmd.logger", loggerLoader)
	preload("fmd.fileutil", fileutilLoader)
	preload("fmd.gzip", gzipLoader)
	preload("fmd.duktape", duktapeLoader(luaDir))
	preload("fmd.imagepuzzle", imagepuzzleLoader)
	preload("fmd.mangafoxwatermark", mangafoxwatermarkLoader)

	// Capabilities that are not implemented yet. Registering a stub that raises
	// keeps the failure loud and specific: the module that needs it names itself
	// in the error rather than silently returning empty fields.
	for name, need := range map[string]string{
		"fmd.subprocess": "external process execution",
	} {
		preload(name, unsupportedLoader(name, need))
	}
}

func unsupportedLoader(lib, need string) lua.LGFunction {
	return func(L *lua.LState) int {
		mt := L.NewTable()
		L.SetMetatable(mt, L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
			"__index": func(L *lua.LState) int {
				fn := L.CheckString(2)
				L.Push(L.NewFunction(func(L *lua.LState) int {
					L.RaiseError("%s.%s requires %s, which atsume does not implement yet", lib, fn, need)
					return 0
				}))
				return 1
			},
		}))
		L.Push(mt)
		return 1
	}
}

func cryptoLoader(L *lua.LState) int {
	fns := map[string]lua.LGFunction{
		// EncodeURLElement percent-encodes every reserved character, which is
		// what modules rely on when splicing a title into a query string.
		"EncodeURLElement": str1(func(s string) string {
			return strings.ReplaceAll(url.QueryEscape(s), "+", "%20")
		}),
		"EncodeURL":  str1(func(s string) string { return (&url.URL{Path: s}).EscapedPath() }),
		"DecodeURL":  str1(func(s string) string { v, _ := url.QueryUnescape(s); return v }),
		"HTMLEncode": str1(html.EscapeString),
		"HTMLDecode": str1(html.UnescapeString),
		"EncodeBase64": str1(func(s string) string {
			return base64.StdEncoding.EncodeToString([]byte(s))
		}),
		"DecodeBase64": str1(func(s string) string {
			// sites are inconsistent about padding, so accept either form
			if v, err := base64.StdEncoding.DecodeString(s); err == nil {
				return string(v)
			}
			v, _ := base64.RawStdEncoding.DecodeString(s)
			return string(v)
		}),
		"HexToStr": str1(func(s string) string { v, _ := hex.DecodeString(s); return string(v) }),
		"StrToHex": str1(func(s string) string { return hex.EncodeToString([]byte(s)) }),
		"SHA256": str1(func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return string(sum[:])
		}),
		"SHA256Hex": str1(func(s string) string {
			sum := sha256.Sum256([]byte(s))
			return hex.EncodeToString(sum[:])
		}),
		"HMAC_SHA256": func(L *lua.LState) int {
			L.Push(lua.LString(hmacSHA256(L.CheckString(1), L.CheckString(2))))
			return 1
		},
		"HMAC_SHA256Hex": func(L *lua.LState) int {
			L.Push(lua.LString(hex.EncodeToString([]byte(hmacSHA256(L.CheckString(1), L.CheckString(2))))))
			return 1
		},
		"AESCTR": func(L *lua.LState) int {
			out, err := aesCTR([]byte(L.CheckString(1)), []byte(L.CheckString(2)), []byte(L.CheckString(3)))
			if err != nil {
				L.RaiseError("fmd.crypto.AESCTR: %v", err)
				return 0
			}
			L.Push(lua.LString(out))
			return 1
		},
	}
	L.Push(L.SetFuncs(L.NewTable(), fns))
	return 1
}

// str1 adapts a single-argument string function to a Lua binding.
func str1(fn func(string) string) lua.LGFunction {
	return func(L *lua.LState) int {
		L.Push(lua.LString(fn(L.CheckString(1))))
		return 1
	}
}

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

func envLoader(luaDir string) lua.LGFunction {
	return func(L *lua.LState) int {
		t := L.NewTable()
		// Modules branch on this to pick a language-specific directory or parser.
		L.SetField(t, "SelectedLanguage", lua.LString("en"))
		// Upstream ends this with a path separator, and modules concatenate
		// straight onto it: fmd.LuaDirectory .. 'extras\\mangafoxtemplate'.
		// Without the separator the result is a path that does not exist,
		// and the module carries on as though the directory were empty.
		L.SetField(t, "LuaDirectory", lua.LString(luaDir+string(os.PathSeparator)))
		L.Push(t)
		return 1
	}
}

func loggerLoader(L *lua.LState) int {
	L.Push(L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
		"Send": func(L *lua.LState) int {
			slog.Debug("lua module", "msg", L.CheckString(1))
			return 0
		},
		"SendError": func(L *lua.LState) int {
			slog.Warn("lua module", "msg", L.CheckString(1))
			return 0
		},
	}))
	return 1
}

func fileutilLoader(L *lua.LState) int {
	L.Push(L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
		"ExtractFileNameOnly": str1(func(s string) string {
			s = s[strings.LastIndexAny(s, `/\`)+1:]
			if i := strings.LastIndex(s, "."); i > 0 {
				s = s[:i]
			}
			return s
		}),
	}))
	return 1
}

func gzipLoader(L *lua.LState) int {
	L.Push(L.SetFuncs(L.NewTable(), map[string]lua.LGFunction{
		"Decompress": func(L *lua.LState) int {
			zr, err := gzip.NewReader(bytes.NewReader([]byte(L.CheckString(1))))
			if err != nil {
				L.RaiseError("fmd.gzip.Decompress: %v", err)
				return 0
			}
			defer zr.Close()
			out, err := io.ReadAll(zr)
			if err != nil {
				L.RaiseError("fmd.gzip.Decompress: %v", err)
				return 0
			}
			L.Push(lua.LString(out))
			return 1
		},
	}))
	return 1
}
