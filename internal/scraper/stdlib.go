package scraper

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arnodel/golua/lib/base"
	"github.com/arnodel/golua/lib/coroutine"
	"github.com/arnodel/golua/lib/mathlib"
	"github.com/arnodel/golua/lib/oslib"
	"github.com/arnodel/golua/lib/packagelib"
	"github.com/arnodel/golua/lib/stringlib"
	"github.com/arnodel/golua/lib/tablelib"
	"github.com/arnodel/golua/lib/utf8lib"
	rt "github.com/arnodel/golua/runtime"
)

// newLuaRuntime returns a golua runtime holding the parts of the standard
// library a website module may use.
//
// Modules are fetched from upstream at run time, so they are treated as
// untrusted code. The libraries are therefore listed rather than taken whole:
// golib (imports arbitrary Go packages), runtimelib (adjusts its own resource
// limits) and debuglib (reaches into other functions' upvalues and the
// registry) are left out, and io and os are replaced by the narrow versions
// below. What remains is what the FMD2 modules actually call.
//
// luaDir is the checkout's lua directory. require loads shared code from it,
// and file reads resolve relative paths against the checkout root above it,
// FMD2's working directory. Neither can leave the checkout.
//
// Every Go function exposed here is declared CPU-safe (see setGoFunc), so a
// handler can run under a CPU limit.
func newLuaRuntime(stdout io.Writer, luaDir string) *rt.Runtime {
	root := filepath.Dir(luaDir)
	r := rt.New(stdout)
	for _, l := range []packagelib.Loader{
		base.LibLoader,
		packagelib.LibLoader,
		coroutine.LibLoader,
		stringlib.LibLoader,
		tablelib.LibLoader,
		mathlib.LibLoader,
		utf8lib.LibLoader,
		oslib.LibLoader,
		{Name: "io", Load: func(r *rt.Runtime) (rt.Value, func()) { return rt.TableValue(ioTable(r, root)), nil }},
	} {
		l.Run(r)
	}

	env := r.GlobalEnv()
	// dofile and loadfile read any path on disk; require covers the only
	// legitimate use, loading shared code from the checkout.
	env.Set(rt.StringValue("dofile"), rt.NilValue)
	env.Set(rt.StringValue("loadfile"), rt.NilValue)

	// golua's own require is replaced: it reads whatever package.path names,
	// anywhere on disk, and is not declared CPU-safe, so it would fail under
	// the limit handlers run with. No module reads or sets package.path, and
	// searchpath goes with it.
	pkg := env.Get(rt.StringValue("package")).AsTable()
	pkg.Set(rt.StringValue("path"), rt.NilValue)
	pkg.Set(rt.StringValue("searchpath"), rt.NilValue)
	setGoFunc(r, env, "require", requireFunc(pkg, luaDir), 1, false)

	narrowOS(r, env.Get(rt.StringValue("os")).AsTable())
	return r
}

// setGoFunc sets a Go function in a table and declares it CPU-safe.
//
// golua refuses, inside a CPU-limited call, any Go function not declared
// safe. The declaration promises the function's own work is bounded by its
// input. That holds for every binding here: none loops on the module's
// behalf, and the limit exists to stop loops written in Lua.
func setGoFunc(r *rt.Runtime, t *rt.Table, name string, fn rt.GoFunctionFunc, nArgs int, hasEtc bool) *rt.GoFunction {
	f := r.SetEnvGoFunc(t, name, fn, nArgs, hasEtc)
	rt.SolemnlyDeclareCompliance(rt.ComplyCpuSafe, f)
	return f
}

// newGoFunc makes a Go function value declared CPU-safe; see setGoFunc.
func newGoFunc(fn rt.GoFunctionFunc, name string, nArgs int, hasEtc bool) *rt.GoFunction {
	f := rt.NewGoFunction(fn, name, nArgs, hasEtc)
	rt.SolemnlyDeclareCompliance(rt.ComplyCpuSafe, f)
	return f
}

// requireFunc implements require for module code.
//
// It looks in package.loaded, then package.preload (the fmd.* libraries),
// then for name.lua or name/init.lua under luaDir, with dots as directory
// separators. Files are opened with os.OpenInRoot, so a name cannot reach
// outside luaDir, and are loaded as text only: the checkout holds no
// precompiled chunks.
func requireFunc(pkg *rt.Table, luaDir string) rt.GoFunctionFunc {
	loadedKey, preloadKey := rt.StringValue("loaded"), rt.StringValue("preload")
	return func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, err := c.StringArg(0)
		if err != nil {
			return nil, err
		}
		loaded := pkg.Get(loadedKey).AsTable()
		if v := loaded.Get(rt.StringValue(name)); !v.IsNil() {
			return c.PushingNext1(t.Runtime, v), nil
		}

		var loader, extra rt.Value
		if l := pkg.Get(preloadKey).AsTable().Get(rt.StringValue(name)); !l.IsNil() {
			loader, extra = l, rt.StringValue(":preload:")
		} else {
			rel := filepath.FromSlash(strings.ReplaceAll(name, ".", "/"))
			var tried []string
			for _, candidate := range []string{rel + ".lua", filepath.Join(rel, "init.lua")} {
				src, err := readInRoot(luaDir, candidate)
				if err != nil {
					tried = append(tried, "\n\tno file '"+filepath.Join(luaDir, candidate)+"'")
					continue
				}
				path := filepath.Join(luaDir, candidate)
				chunk, err := t.LoadFromSourceOrCode(path, src, "t", rt.TableValue(t.GlobalEnv()), true)
				if err != nil {
					return nil, fmt.Errorf("error loading module '%s' from file '%s':\n\t%w", name, path, err)
				}
				loader, extra = rt.FunctionValue(chunk), rt.StringValue(path)
				break
			}
			if loader.IsNil() {
				return nil, fmt.Errorf("module '%s' not found:\n\tno field package.preload['%s']%s",
					name, name, strings.Join(tried, ""))
			}
		}

		v, err := rt.Call1(t, loader, rt.StringValue(name), extra)
		if err != nil {
			return nil, err
		}
		// A module that returns nothing may have filled package.loaded itself;
		// otherwise it is recorded as true, as Lua does.
		if v.IsNil() {
			if v = loaded.Get(rt.StringValue(name)); v.IsNil() {
				v = rt.BoolValue(true)
			}
		}
		loaded.Set(rt.StringValue(name), v)
		return c.PushingNext(t.Runtime, v, extra), nil
	}
}

// narrowOS keeps the clock functions and refuses the ones that touch the
// filesystem or the process.
//
// remove and rename stay present but fail the way Lua reports a failed file
// operation, with nil and a message. utils/nodejs.lua uses rename as an
// existence check, and a missing function would turn that into an error.
func narrowOS(r *rt.Runtime, os *rt.Table) {
	for _, name := range []string{"execute", "exit", "getenv", "setlocale", "tmpname"} {
		os.Set(rt.StringValue(name), rt.NilValue)
	}
	refuse := func(name string) {
		setGoFunc(r, os, name, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			path, _ := c.StringArg(0)
			return c.PushingNext(t.Runtime, rt.NilValue, rt.StringValue(path+": operation not permitted")), nil
		}, 2, false)
	}
	refuse("remove")
	refuse("rename")
}

// ioTable builds a read-only io library confined to root.
//
// Only io.open and io.lines are provided, since those are all the modules
// read with. Opening for writing fails with nil and a message, as a read-only
// filesystem would: MangaDex's legacy-ID cache and the Node.js helper are the
// only writers, and neither can do its job in atsume anyway.
func ioTable(r *rt.Runtime, root string) *rt.Table {
	meta := luaFileMeta()
	pkg := rt.NewTable()
	setGoFunc(r, pkg, "open", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, err := c.StringArg(0)
		if err != nil {
			return nil, err
		}
		mode := "r"
		if c.NArgs() > 1 {
			if mode, err = c.StringArg(1); err != nil {
				return nil, err
			}
		}
		if strings.ContainsAny(mode, "wa+") {
			return c.PushingNext(t.Runtime, rt.NilValue, rt.StringValue(name+": read-only file system")), nil
		}
		data, err := readInRoot(root, name)
		if err != nil {
			return c.PushingNext(t.Runtime, rt.NilValue, rt.StringValue(err.Error())), nil
		}
		return c.PushingNext1(t.Runtime, rt.UserDataValue(rt.NewUserData(&luaFile{data: data}, meta))), nil
	}, 2, false)

	setGoFunc(r, pkg, "lines", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		name, err := c.StringArg(0)
		if err != nil {
			return nil, err
		}
		data, err := readInRoot(root, name)
		if err != nil {
			return nil, err
		}
		f := &luaFile{data: data}
		next := newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			return c.PushingNext1(t.Runtime, f.readLine(false)), nil
		}, "lines", 0, false)
		return c.PushingNext1(t.Runtime, rt.FunctionValue(next)), nil
	}, 1, false)
	return pkg
}

// readInRoot reads name, resolved against root when relative. os.OpenInRoot
// refuses anything that leaves root, including through a symlink or "..", so
// a module cannot read outside the checkout.
func readInRoot(root, name string) ([]byte, error) {
	rel := name
	if filepath.IsAbs(name) {
		var err error
		if rel, err = filepath.Rel(root, name); err != nil {
			return nil, fmt.Errorf("%s: permission denied", name)
		}
	}
	f, err := os.OpenInRoot(root, rel)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%s: No such file or directory", name)
		}
		return nil, fmt.Errorf("%s: permission denied", name)
	}
	defer f.Close()
	return io.ReadAll(f)
}

// luaFile is a file opened by io.open. The content is read up front: module
// files are small, and it keeps no descriptor open between handler calls.
type luaFile struct {
	data   []byte
	pos    int
	closed bool
}

// luaFileMeta builds the metatable for io.open's files. Each runtime gets its
// own: runtimes run concurrently in the worker pool, and a golua table is not
// safe to share between them.
func luaFileMeta() *rt.Table {
	methods := rt.NewTable()
	set := func(name string, nArgs int, fn rt.GoFunctionFunc) {
		methods.Set(rt.StringValue(name), rt.FunctionValue(newGoFunc(fn, name, nArgs, true)))
	}
	set("read", 1, fileRead)
	set("lines", 1, fileLines)
	set("close", 1, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		f, err := toLuaFile(c)
		if err != nil {
			return nil, err
		}
		f.closed = true
		return c.PushingNext1(t.Runtime, rt.BoolValue(true)), nil
	})
	meta := rt.NewTable()
	meta.Set(rt.StringValue("__index"), rt.TableValue(methods))
	meta.Set(rt.StringValue("__name"), rt.StringValue("file"))
	return meta
}

func toLuaFile(c *rt.GoCont) (*luaFile, error) {
	u, err := c.UserDataArg(0)
	if err != nil {
		return nil, err
	}
	f, ok := u.Value().(*luaFile)
	if !ok {
		return nil, errors.New("#1 must be a file")
	}
	if f.closed {
		return nil, errors.New("attempt to use a closed file")
	}
	return f, nil
}

// fileRead implements file:read for the formats modules use: "a" (the rest of
// the file), "l" and "L" (a line without or with its newline), each with or
// without the Lua 5.1 "*" prefix. Several formats return several values.
func fileRead(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	f, err := toLuaFile(c)
	if err != nil {
		return nil, err
	}
	formats := c.Etc()
	if len(formats) == 0 {
		formats = []rt.Value{rt.StringValue("l")}
	}
	next := c.Next()
	for _, v := range formats {
		format, ok := v.TryString()
		if !ok {
			return nil, errors.New("invalid format")
		}
		var out rt.Value
		switch strings.TrimPrefix(format, "*") {
		case "a", "all":
			out = rt.StringValue(string(f.data[f.pos:]))
			f.pos = len(f.data)
		case "l", "line":
			out = f.readLine(false)
		case "L":
			out = f.readLine(true)
		default:
			return nil, fmt.Errorf("invalid format %q", format)
		}
		t.Push1(next, out)
		if out.IsNil() {
			break
		}
	}
	return next, nil
}

func fileLines(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	f, err := toLuaFile(c)
	if err != nil {
		return nil, err
	}
	next := newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		return c.PushingNext1(t.Runtime, f.readLine(false)), nil
	}, "lines", 0, false)
	return c.PushingNext1(t.Runtime, rt.FunctionValue(next)), nil
}

// readLine returns the next line, or nil at the end of the file. A trailing
// "\r" is dropped along with the "\n", since FMD2 ran on Windows and its
// files may carry CRLF endings.
func (f *luaFile) readLine(keepNewline bool) rt.Value {
	if f.pos >= len(f.data) {
		return rt.NilValue
	}
	rest := f.data[f.pos:]
	i := bytes.IndexByte(rest, '\n')
	if i < 0 {
		f.pos = len(f.data)
		return rt.StringValue(string(rest))
	}
	f.pos += i + 1
	if keepNewline {
		return rt.StringValue(string(rest[:i+1]))
	}
	return rt.StringValue(strings.TrimSuffix(string(rest[:i]), "\r"))
}
