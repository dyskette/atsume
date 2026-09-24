package scraper

import (
	"fmt"

	rt "github.com/arnodel/golua/runtime"
)

// fields describes a Lua-visible object whose properties are backed directly
// by Go variables. FMD2 modules read and write the injected objects as plain
// records (MANGAINFO.Title = ..., TASK.PageNumber, HTTP.MimeType), so binding
// pointers is both the shortest and the most faithful mapping. MANGAINFO,
// TASK, HTTP and MODULE are all built on it.
type fields struct {
	typeName string
	str      map[string]*string
	num      map[string]*int
	boolean  map[string]*bool
	list     map[string]*Strings
	methods  map[string]goFn
	// getter and setter handle properties that need computation rather than a
	// backing variable.
	getter map[string]func(t *rt.Thread) rt.Value
	setter map[string]func(t *rt.Thread, v rt.Value) error

	// bound caches the function values built for methods, so reading
	// HTTP.GET twice gives the same function.
	bound map[string]rt.Value
}

func newFields(typeName string) *fields {
	return &fields{
		typeName: typeName,
		str:      map[string]*string{},
		num:      map[string]*int{},
		boolean:  map[string]*bool{},
		list:     map[string]*Strings{},
		methods:  map[string]goFn{},
		getter:   map[string]func(t *rt.Thread) rt.Value{},
		setter:   map[string]func(t *rt.Thread, v rt.Value) error{},
		bound:    map[string]rt.Value{},
	}
}

// push returns userdata bound to f.
func (f *fields) push(r *rt.Runtime) rt.Value {
	meta := typeMeta(r, "atsume.fields", func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(fieldsIndex, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(fieldsNewIndex, "__newindex", 3, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(f, meta))
}

// typeMeta returns the metatable registered under name in r, building it on
// first use: one per runtime, kept in the registry, because golua tables are
// not safe to share between runtimes that run concurrently.
func typeMeta(r *rt.Runtime, name string, build func() *rt.Table) *rt.Table {
	key := rt.StringValue("atsume.meta." + name)
	if mt, ok := r.Registry(key).TryTable(); ok {
		return mt
	}
	mt := build()
	mt.Set(rt.StringValue("__name"), rt.StringValue(name))
	r.SetRegistry(key, rt.TableValue(mt))
	return mt
}

func toFields(c *rt.GoCont) (*fields, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if f, ok := u.Value().(*fields); ok {
			return f, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (object expected, got %s)", c.Arg(0).TypeName())
}

func fieldsIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	f, err := toFields(c)
	if err != nil {
		return nil, err
	}
	key, err := checkString(c, 1)
	if err != nil {
		return nil, err
	}
	var v rt.Value
	switch {
	case f.str[key] != nil:
		v = rt.StringValue(*f.str[key])
	case f.num[key] != nil:
		// An integer, not a float: under Lua 5.5 rules a float would read
		// "2.0" when a module splices it into a URL.
		v = rt.IntValue(int64(*f.num[key]))
	case f.boolean[key] != nil:
		v = rt.BoolValue(*f.boolean[key])
	case f.list[key] != nil:
		v = pushStrings(t.Runtime, f.list[key])
	case f.methods[key].fn != nil:
		if v = f.bound[key]; v.IsNil() {
			m := f.methods[key]
			v = rt.FunctionValue(newGoFunc(m.fn, key, m.nArgs, false))
			f.bound[key] = v
		}
	case f.getter[key] != nil:
		v = f.getter[key](t)
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// fieldsNewIndex writes a property, converting the value the way modules
// expect: a number assigned to a string field becomes its text, anything but
// a number reads as 0 in a number field, and a boolean field takes Lua
// truthiness. Unknown keys are ignored.
func fieldsNewIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	f, err := toFields(c)
	if err != nil {
		return nil, err
	}
	key, err := checkString(c, 1)
	if err != nil {
		return nil, err
	}
	v := c.Arg(2)
	switch {
	case f.str[key] != nil:
		*f.str[key] = luaString(v)
	case f.num[key] != nil:
		*f.num[key] = truncInt(v)
	case f.boolean[key] != nil:
		*f.boolean[key] = rt.Truth(v)
	case f.setter[key] != nil:
		if err := f.setter[key](t, v); err != nil {
			return nil, err
		}
	}
	return c.Next(), nil
}

// luaMethod builds a method bound to a Go receiver the closure captures.
// Modules always use dot notation (LINKS.Add(x), x.XPathString(e)), never
// colon, so arguments start with the first real one. A nil result returns
// nothing.
func luaMethod(name string, nArgs int, fn func(t *rt.Thread, c *rt.GoCont) (rt.Value, error)) rt.Value {
	return rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		v, err := fn(t, c)
		if err != nil {
			return nil, err
		}
		if v.IsNil() {
			return c.Next(), nil
		}
		return c.PushingNext1(t.Runtime, v), nil
	}, name, nArgs, false))
}

// optString reads optional argument n as a string, or def when it is absent
// or nil.
func optString(c *rt.GoCont, n int, def string) (string, error) {
	if c.Arg(n).IsNil() {
		return def, nil
	}
	return checkString(c, n)
}

// luaString reads a string or number as text, and anything else as "".
func luaString(v rt.Value) string {
	switch v.Type() {
	case rt.StringType, rt.IntType, rt.FloatType:
		s, _ := v.ToString()
		return s
	}
	return ""
}

// truncInt reads a number, or a string holding one, truncated toward zero.
// Anything else is 0.
func truncInt(v rt.Value) int {
	n, f, tp := rt.ToNumber(v)
	switch tp {
	case rt.IsInt:
		return int(n)
	case rt.IsFloat:
		return int(f)
	}
	return 0
}

// noResult adapts a Go function with no Lua result.
func noResult(nArgs int, fn func(c *rt.GoCont) error) goFn {
	return goFn{nArgs, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		if err := fn(c); err != nil {
			return nil, err
		}
		return c.Next(), nil
	}}
}
