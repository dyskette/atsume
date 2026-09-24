package scraper

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// jsTimeout bounds one ExecJS call.
//
// The script comes from a scraped page, so it is untrusted input: an infinite
// loop in it would otherwise pin a worker goroutine forever. goja has no
// preemption, so the only way out is an explicit interrupt.
const jsTimeout = 15 * time.Second

// jsRuntime executes the JavaScript that modules extract from pages.
//
// FMD2 embeds Duktape for this. goja fills the same role: a sandboxed
// interpreter with no filesystem, network or process access of its own. The
// only capability granted is require(), which is restricted to the .js helpers
// shipped in the module checkout.
type jsRuntime struct {
	// luaDir is the module checkout's lua directory, the root that require()
	// resolves against.
	luaDir  string
	timeout time.Duration
}

func newJSRuntime(luaDir string) *jsRuntime {
	return &jsRuntime{luaDir: luaDir, timeout: jsTimeout}
}

// Exec evaluates src and returns its completion value as a string.
//
// Modules end their scripts with the expression they want back, usually a
// JSON.stringify call, and read the result as text.
func (j *jsRuntime) Exec(src string) (string, error) {
	vm := goja.New()
	if err := j.install(vm); err != nil {
		return "", err
	}

	// Interrupt fires on another goroutine; stop the timer on the way out so a
	// fast script does not leave one pending.
	timer := time.AfterFunc(j.timeout, func() {
		vm.Interrupt(fmt.Sprintf("execution exceeded %s", j.timeout))
	})
	defer func() {
		timer.Stop()
		vm.ClearInterrupt()
	}()

	v, err := vm.RunString(src)
	if err != nil {
		return "", fmt.Errorf("ExecJS: %w", err)
	}
	return jsString(v), nil
}

// jsString renders a completion value the way a module expects to read it.
//
// This follows Duktape.pas: the result is duk_safe_to_string of the completion
// value, with the single special case that "undefined" becomes the empty
// string. Notably null is *not* special-cased and reads back as "null", and an
// array renders through Array.prototype.toString rather than as JSON — modules
// are written against both behaviours.
func jsString(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return ""
	}
	return v.String()
}

// install adds the globals a scraped script may reasonably expect. Nothing here
// reaches outside the process.
func (j *jsRuntime) install(vm *goja.Runtime) error {
	cache := map[string]goja.Value{}

	if err := vm.Set("require", func(call goja.FunctionCall) goja.Value {
		name := call.Argument(0).String()
		v, err := j.require(vm, name, cache)
		if err != nil {
			panic(vm.ToValue(vm.NewGoError(err)))
		}
		return v
	}); err != nil {
		return err
	}

	// FMD2 registers a bare print(); scripts lifted from pages may call it.
	if err := vm.Set("print", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, a.String())
		}
		slog.Debug("module javascript", "level", "print", "msg", strings.Join(parts, " "))
		return goja.Undefined()
	}); err != nil {
		return err
	}

	// crypto-js and its helpers call console.warn on unusual input. Upstream has
	// no console at all and dies on what it treats as a warning; providing one
	// is a deliberate improvement rather than a parity gap.
	console := vm.NewObject()
	for _, level := range []string{"log", "warn", "error", "info", "debug"} {
		name := level
		if err := console.Set(name, func(call goja.FunctionCall) goja.Value {
			parts := make([]string, 0, len(call.Arguments))
			for _, a := range call.Arguments {
				parts = append(parts, a.String())
			}
			slog.Debug("module javascript", "level", name, "msg", strings.Join(parts, " "))
			return goja.Undefined()
		}); err != nil {
			return err
		}
	}
	return vm.Set("console", console)
}

// require loads one of the .js helpers from the module checkout, evaluating it
// as a CommonJS module and caching the result per VM.
//
// The helpers are UMD bundles that detect CommonJS and assign module.exports,
// which is why the wrapper supplies exports, module and require rather than
// simply evaluating the file.
func (j *jsRuntime) require(vm *goja.Runtime, name string, cache map[string]goja.Value) (goja.Value, error) {
	if name == "crypto" {
		return cryptoModule(vm)
	}

	path, err := j.resolve(name)
	if err != nil {
		return nil, err
	}
	if v, ok := cache[path]; ok {
		return v, nil
	}

	src, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("require(%q): %w", name, err)
	}

	module := vm.NewObject()
	exports := vm.NewObject()
	if err := module.Set("exports", exports); err != nil {
		return nil, err
	}

	// The wrapper is an expression so that RunString hands back the function.
	wrapper, err := vm.RunString("(function (exports, module, require) {\n" + string(src) + "\n})")
	if err != nil {
		return nil, fmt.Errorf("require(%q): %w", name, err)
	}
	fn, ok := goja.AssertFunction(wrapper)
	if !ok {
		return nil, fmt.Errorf("require(%q): wrapper is not callable", name)
	}
	if _, err := fn(goja.Undefined(), exports, module, vm.Get("require")); err != nil {
		return nil, fmt.Errorf("require(%q): %w", name, err)
	}

	result := module.Get("exports")
	cache[path] = result
	return result, nil
}

// cryptoModule supplies the randomness source crypto-js looks for.
//
// crypto-js probes window.crypto, then global.crypto, then require("crypto"),
// and throws outright if none answers. Serving it here rather than defining a
// `global` object is deliberate: scraped scripts branch on `typeof global` to
// detect Node, and inventing one would push them down a path that cannot work.
func cryptoModule(vm *goja.Runtime) (goja.Value, error) {
	mod := vm.NewObject()

	// crypto-js calls getRandomValues(new Uint32Array(1))[0] first.
	if err := mod.Set("getRandomValues", func(call goja.FunctionCall) goja.Value {
		arr, ok := call.Argument(0).(*goja.Object)
		if !ok {
			panic(vm.NewTypeError("getRandomValues expects a typed array"))
		}
		n := int(arr.Get("length").ToInteger())
		for i := 0; i < n; i++ {
			if err := arr.Set(strconv.Itoa(i), vm.ToValue(randomUint32())); err != nil {
				panic(vm.ToValue(vm.NewGoError(err)))
			}
		}
		return arr
	}); err != nil {
		return nil, err
	}

	// ...and randomBytes(4).readInt32LE() as its fallback.
	if err := mod.Set("randomBytes", func(call goja.FunctionCall) goja.Value {
		n := int(call.Argument(0).ToInteger())
		if n < 0 || n > 1<<20 {
			panic(vm.NewTypeError("randomBytes: implausible length"))
		}
		buf := make([]byte, n)
		if _, err := rand.Read(buf); err != nil {
			panic(vm.ToValue(vm.NewGoError(err)))
		}
		out := vm.NewObject()
		_ = out.Set("length", n)
		for i, b := range buf {
			_ = out.Set(strconv.Itoa(i), int64(b))
		}
		_ = out.Set("readInt32LE", func(goja.FunctionCall) goja.Value {
			if len(buf) < 4 {
				return vm.ToValue(0)
			}
			return vm.ToValue(int32(binary.LittleEndian.Uint32(buf[:4])))
		})
		return out
	}); err != nil {
		return nil, err
	}
	return mod, nil
}

// randomUint32 returns a cryptographically secure 32-bit value.
func randomUint32() uint32 {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand does not fail on any platform atsume runs on; if it ever
		// does, failing loudly beats handing out predictable keys.
		panic(fmt.Sprintf("crypto/rand unavailable: %v", err))
	}
	return binary.LittleEndian.Uint32(b[:])
}

// resolve maps a require name onto a file inside the checkout.
//
// The name comes from a scraped page, so it is rejected unless it stays inside
// luaDir and names a .js file. Without this check a page could ask the host to
// read an arbitrary file.
func (j *jsRuntime) resolve(name string) (string, error) {
	if j.luaDir == "" {
		return "", fmt.Errorf("require(%q): no module directory configured", name)
	}
	// Modules write both require("utils/crypto-js.min.js") and dotted forms.
	clean := strings.ReplaceAll(name, "\\", "/")
	if !strings.HasSuffix(clean, ".js") {
		clean = strings.ReplaceAll(clean, ".", "/") + ".js"
	}

	root, err := filepath.Abs(j.luaDir)
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(clean))
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("require(%q): refers outside the module directory", name)
	}
	if _, err := os.Stat(full); err != nil {
		return "", fmt.Errorf("require(%q): %w", name, err)
	}
	return full, nil
}
