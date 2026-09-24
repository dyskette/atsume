package scraper

import (
	"math"

	rt "github.com/arnodel/golua/runtime"
)

// OptionKind is the widget a module asks for when declaring a setting.
type OptionKind string

const (
	OptionCheckBox OptionKind = "checkbox"
	OptionSpinEdit OptionKind = "spinedit"
	OptionComboBox OptionKind = "combobox"
	OptionEditBox  OptionKind = "editbox"
)

// Option is one module-declared setting. The UI is generated from these rather
// than hand-written per module.
type Option struct {
	Kind    OptionKind
	Name    string
	Caption string
	// Default is what the module declared, as a plain Go value: nil, bool,
	// int64, float64 or string. It is kept free of any Lua runtime's types so
	// that callers outside this package never depend on one.
	Default any
	Items   []string
}

// Module is a loaded website module: the metadata its Init() declared plus the
// names of the Lua functions handling each event.
type Module struct {
	ID       string
	Name     string
	RootURL  string
	Category string

	// Handlers maps an event name such as "OnGetInfo" to the Lua function name
	// the module assigned to it.
	Handlers map[string]string

	Options        []Option
	AccountSupport bool
	SortedList     bool
	TotalDirectory int

	// File is the path the module was loaded from, for diagnostics.
	File string
}

// Handler returns the Lua function name for an event, and whether it is set.
func (m *Module) Handler(event string) (string, bool) {
	name, ok := m.Handlers[event]
	return name, ok && name != ""
}

// moduleEvents are the handler slots a module may assign in Init().
var moduleEvents = []string{
	"OnGetNameAndLink", "OnGetInfo", "OnGetPageNumber", "OnGetDirectoryPageNumber",
	"OnGetImageURL", "OnBeforeDownloadImage", "OnDownloadImage", "OnAfterImageSaved",
	"OnSaveImage", "OnLogin", "OnTaskStart", "OnCheckSite", "OnAccountState",
	"OnBeforeUpdateList", "OnAfterUpdateList",
}

// newWebsiteModule builds the table NewWebsiteModule returns: the option
// setters and the Storage scratchpad. Modules call the setters with a dot, so
// the first argument is the option name, not the table.
func newWebsiteModule(r *rt.Runtime, opts *[]Option, storage map[string]string) *rt.Table {
	t := rt.NewTable()
	t.Set(rt.StringValue("Storage"), pushStorage(storage))
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
				// Only a table of items is read. The newline-separated string
				// form some modules pass ('Auto\nOriginal') leaves Items
				// empty, so those combo boxes offer no choices.
				if items, ok := c.Arg(2).TryTable(); ok {
					for i := int64(1); i <= items.Len(); i++ {
						s, _ := items.Get(rt.IntValue(i)).ToString()
						o.Items = append(o.Items, s)
					}
				}
				defaultArg = 3
			}
			o.Default = optionValue(c.Arg(defaultArg))
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
		setGoFunc(r, t, name, add(kind), 4, false)
	}
	return t
}

// pushStorage exposes a string map as Storage['key']. A missing key reads as
// "" rather than nil, because templates compare it with the empty string.
func pushStorage(m map[string]string) rt.Value {
	meta := rt.NewTable()
	meta.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		key, err := checkString(c, 1)
		if err != nil {
			return nil, err
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(m[key])), nil
	}, "__index", 2, false)))
	meta.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
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

// readModule reads a declaration table into a Module. Text fields accept a
// number, as Lua would; flags take Lua truthiness.
func readModule(t *rt.Table, opts []Option, file string) *Module {
	m := &Module{Handlers: map[string]string{}, File: file, Options: opts}
	m.ID = luaStr(t, "ID")
	m.Name = luaStr(t, "Name")
	m.RootURL = luaStr(t, "RootURL")
	m.Category = luaStr(t, "Category")
	m.AccountSupport = rt.Truth(t.Get(rt.StringValue("AccountSupport")))
	m.SortedList = rt.Truth(t.Get(rt.StringValue("SortedList")))
	if n, ok := rt.ToFloat(t.Get(rt.StringValue("TotalDirectory"))); ok {
		m.TotalDirectory = int(n)
	}
	for _, ev := range moduleEvents {
		if v := luaStr(t, ev); v != "" {
			m.Handlers[ev] = v
		}
	}
	return m
}

// luaStr reads a string field. A number is converted, as Lua would; any
// other type reads as empty.
func luaStr(t *rt.Table, key string) string {
	v := t.Get(rt.StringValue(key))
	switch v.Type() {
	case rt.StringType, rt.IntType, rt.FloatType:
		s, _ := v.ToString()
		return s
	}
	return ""
}

// optionValue converts a declared default to a plain Go value. A float
// holding a whole number becomes an int64, so a default written 1.0 and one
// written 1 read the same on the settings page.
func optionValue(v rt.Value) any {
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

// luaValueOf is optionValue's inverse, for handing a default back through
// MODULE.GetOption.
func luaValueOf(v any) rt.Value {
	switch v := v.(type) {
	case bool:
		return rt.BoolValue(v)
	case int64:
		return rt.IntValue(v)
	case float64:
		return rt.FloatValue(v)
	case string:
		return rt.StringValue(v)
	}
	return rt.NilValue
}
