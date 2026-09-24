package scraper

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"

	rt "github.com/arnodel/golua/runtime"
)

// goluaRunner is the golua counterpart of Runner, built up alongside it while
// the bindings move over. When the port is complete it replaces Runner and
// gopher-lua goes; until then Host.Open keeps using gopher-lua.
type goluaRunner struct {
	r *rt.Runtime
	// mod is the website this runner is scraping; sites is everything the
	// file declared, because one file is not one website.
	mod   *Module
	sites []*Module
	// storage backs MODULE.Storage; see Runner.storage.
	storage map[string]string
}

// openGolua loads a module file into a golua runtime and runs its Init, the
// counterpart of Open. Site and rootURL select among the declarations as they
// do there.
func (h *Host) openGolua(ctx context.Context, moduleFile, site, rootURL string) (*goluaRunner, error) {
	// The checkout root, one level above lua/, is FMD2's working directory:
	// module-relative paths such as 'userdata/...' resolve against it.
	r := newLuaRuntime(os.Stdout, filepath.Dir(h.LuaDir))
	gr := &goluaRunner{r: r, storage: map[string]string{}}
	env := r.GlobalEnv()

	// `require 'templates.Madara'` resolves against the checkout, as in Open.
	pkg := env.Get(rt.StringValue("package")).AsTable()
	pkg.Set(rt.StringValue("path"), rt.StringValue(
		filepath.Join(h.LuaDir, "?.lua")+";"+filepath.Join(h.LuaDir, "?", "init.lua")))
	preloadLibs(r, h.LuaDir)

	// Each declaration collects its own options; see the same capture in Open.
	type declaration struct {
		tbl  *rt.Table
		opts []Option
	}
	var decls []*declaration
	r.SetEnvGoFunc(env, "NewWebsiteModule", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		d := &declaration{}
		d.tbl = goluaWebsiteModule(t.Runtime, &d.opts, gr.storage)
		decls = append(decls, d)
		return c.PushingNext1(t.Runtime, rt.TableValue(d.tbl)), nil
	}, 0, true)

	if err := goluaDoFile(r, moduleFile); err != nil {
		return nil, fmt.Errorf("load %s: %w", moduleFile, err)
	}
	ret, err := rt.Call1(r.MainThread(), env.Get(rt.StringValue("Init")))
	if err != nil {
		return nil, fmt.Errorf("Init %s: %w", moduleFile, err)
	}
	// A module that returns its table from Init() is the documented shape.
	if tbl, ok := ret.TryTable(); ok && len(decls) == 0 {
		decls = append(decls, &declaration{tbl: tbl})
	}
	if len(decls) == 0 {
		return nil, fmt.Errorf("%s: Init never called NewWebsiteModule", moduleFile)
	}

	for _, d := range decls {
		gr.sites = append(gr.sites, readGoluaModule(d.tbl, d.opts, moduleFile))
	}
	gr.mod = selectSite(gr.sites, site, rootURL)
	if gr.mod == nil {
		return nil, fmt.Errorf("%s declares no website named %q", moduleFile, site)
	}
	return gr, nil
}

// goluaDoFile loads and runs a module file.
//
// LoadFromSourceOrCode with stripComment skips a UTF-8 byte-order mark and a
// leading "#" line, as Lua's own luaL_loadfile does. Seventeen upstream
// modules start with a byte-order mark. Only text is accepted: a module file
// is never a precompiled chunk.
func goluaDoFile(r *rt.Runtime, path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	chunk, err := r.LoadFromSourceOrCode(path, src, "t", rt.TableValue(r.GlobalEnv()), true)
	if err != nil {
		return err
	}
	_, err = rt.Call1(r.MainThread(), rt.FunctionValue(chunk))
	return err
}

// call invokes the Lua function bound to an event and returns its result.
func (gr *goluaRunner) call(event string) (rt.Value, error) {
	name, ok := gr.mod.Handler(event)
	if !ok {
		return rt.NilValue, fmt.Errorf("module %s has no %s handler", gr.mod.Name, event)
	}
	fn := gr.r.GlobalEnv().Get(rt.StringValue(name))
	if fn.IsNil() {
		return rt.NilValue, fmt.Errorf("module %s declares %s=%q but defines no such function",
			gr.mod.Name, event, name)
	}
	v, err := rt.Call1(gr.r.MainThread(), fn)
	if err != nil {
		return rt.NilValue, fmt.Errorf("%s/%s: %w", gr.mod.Name, name, err)
	}
	return v, nil
}

// goluaWebsiteModule builds the table NewWebsiteModule returns: the option
// setters and the Storage scratchpad. Modules call the setters with a dot, so
// the first argument is the option name, not the table.
func goluaWebsiteModule(r *rt.Runtime, opts *[]Option, storage map[string]string) *rt.Table {
	t := rt.NewTable()
	t.Set(rt.StringValue("Storage"), goluaStorage(storage))
	add := func(kind OptionKind) rt.GoFunctionFunc {
		return func(th *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			name, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			o := Option{Kind: kind, Name: name}
			o.Caption, _ = c.Arg(1).ToString()
			if c.Arg(1).IsNil() {
				o.Caption = ""
			}
			defaultArg := 2
			if kind == OptionComboBox {
				// Only a table of items is read, as in the gopher-lua host;
				// the newline-separated string form leaves Items empty.
				if items, ok := c.Arg(2).TryTable(); ok {
					for i := int64(1); i <= items.Len(); i++ {
						s, _ := items.Get(rt.IntValue(i)).ToString()
						o.Items = append(o.Items, s)
					}
				}
				defaultArg = 3
			}
			o.Default = goluaOptionValue(c.Arg(defaultArg))
			*opts = append(*opts, o)
			return c.Next(), nil
		}
	}
	for name, kind := range map[string]OptionKind{
		"AddOptionCheckBox": OptionCheckBox,
		"AddOptionSpinEdit": OptionSpinEdit,
		"AddOptionComboBox": OptionComboBox,
		"AddOptionEdit":     OptionEditBox,
		"AddOptionEditBox":  OptionEditBox,
	} {
		r.SetEnvGoFunc(t, name, add(kind), 4, false)
	}
	return t
}

// goluaStorage exposes a string map as Storage['key']. A missing key reads as
// "" rather than nil, because templates compare it with the empty string.
func goluaStorage(m map[string]string) rt.Value {
	meta := rt.NewTable()
	meta.Set(rt.StringValue("__index"), rt.FunctionValue(rt.NewGoFunction(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		key, err := checkString(c, 1)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(m[key])), nil
	}, "__index", 2, false)))
	meta.Set(rt.StringValue("__newindex"), rt.FunctionValue(rt.NewGoFunction(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		key, err := checkString(c, 1)
		if err != nil {
			return nil, err
		}
		val, err := checkString(c, 2)
		if err != nil {
			return nil, err
		}
		m[key] = val
		return c.Next(), nil
	}, "__newindex", 3, false)))
	return rt.UserDataValue(rt.NewUserData(m, meta))
}

// readGoluaModule reads a declaration table into a Module, with the same
// conversions readModule applies.
func readGoluaModule(t *rt.Table, opts []Option, file string) *Module {
	m := &Module{Handlers: map[string]string{}, File: file, Options: opts}
	m.ID = goluaStr(t, "ID")
	m.Name = goluaStr(t, "Name")
	m.RootURL = goluaStr(t, "RootURL")
	m.Category = goluaStr(t, "Category")
	m.AccountSupport = rt.Truth(t.Get(rt.StringValue("AccountSupport")))
	m.SortedList = rt.Truth(t.Get(rt.StringValue("SortedList")))
	if n, ok := rt.ToFloat(t.Get(rt.StringValue("TotalDirectory"))); ok {
		m.TotalDirectory = int(n)
	}
	for _, ev := range moduleEvents {
		if v := goluaStr(t, ev); v != "" {
			m.Handlers[ev] = v
		}
	}
	return m
}

// goluaStr reads a string field. A number is converted, as Lua would; any
// other type reads as empty.
func goluaStr(t *rt.Table, key string) string {
	v := t.Get(rt.StringValue(key))
	switch v.Type() {
	case rt.StringType, rt.IntType, rt.FloatType:
		s, _ := v.ToString()
		return s
	}
	return ""
}

// goluaOptionValue converts a declared default to a plain Go value, as
// optionValue does for gopher-lua. A float holding a whole number becomes an
// int64 too, so both runtimes agree on "1" and "1.0".
func goluaOptionValue(v rt.Value) any {
	switch v.Type() {
	case rt.NilType:
		return nil
	case rt.BoolType:
		return v.AsBool()
	case rt.IntType:
		return v.AsInt()
	case rt.FloatType:
		if f := v.AsFloat(); f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f)
		}
		return v.AsFloat()
	case rt.StringType:
		return v.AsString()
	}
	s, _ := v.ToString()
	return s
}
