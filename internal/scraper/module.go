package scraper

import (
	"math"

	lua "github.com/yuin/gopher-lua"
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

// newWebsiteModule builds the table a module's Init() populates. Option setters
// live on the table so `m.AddOptionCheckBox(...)` works as written upstream.
//
// The setters are called with dot notation, so there is no self and the first
// argument is the option name. The signature is (name, caption, default),
// except AddOptionComboBox which takes (name, caption, items, default).
func newWebsiteModule(L *lua.LState, opts *[]Option, storage map[string]string) *lua.LTable {
	t := L.NewTable()
	// Three modules seed MODULE.Storage from inside Init(), so the declaration
	// table exposes the same map the runner will read later.
	L.SetField(t, "Storage", pushStorage(L, storage))
	add := func(kind OptionKind) lua.LGFunction {
		return func(L *lua.LState) int {
			o := Option{
				Kind:    kind,
				Name:    L.CheckString(1),
				Caption: L.OptString(2, ""),
			}
			defaultArg := 3
			if kind == OptionComboBox {
				if items, ok := L.Get(3).(*lua.LTable); ok {
					items.ForEach(func(_, v lua.LValue) { o.Items = append(o.Items, v.String()) })
				}
				defaultArg = 4
			}
			o.Default = optionValue(L.Get(defaultArg))
			*opts = append(*opts, o)
			return 0
		}
	}
	L.SetField(t, "AddOptionCheckBox", L.NewFunction(add(OptionCheckBox)))
	L.SetField(t, "AddOptionSpinEdit", L.NewFunction(add(OptionSpinEdit)))
	L.SetField(t, "AddOptionComboBox", L.NewFunction(add(OptionComboBox)))
	// Upstream's name is AddOptionEdit; LUA-REFERENCE.md calls it AddOptionEditBox.
	L.SetField(t, "AddOptionEdit", L.NewFunction(add(OptionEditBox)))
	L.SetField(t, "AddOptionEditBox", L.NewFunction(add(OptionEditBox)))
	return t
}

// readModule copies the table Init() returned into a Module.
func readModule(t *lua.LTable, opts []Option, file string) *Module {
	m := &Module{Handlers: map[string]string{}, File: file, Options: opts}
	m.ID = luaStr(t, "ID")
	m.Name = luaStr(t, "Name")
	m.RootURL = luaStr(t, "RootURL")
	m.Category = luaStr(t, "Category")
	m.AccountSupport = lua.LVAsBool(t.RawGetString("AccountSupport"))
	m.SortedList = lua.LVAsBool(t.RawGetString("SortedList"))
	m.TotalDirectory = int(lua.LVAsNumber(t.RawGetString("TotalDirectory")))
	for _, ev := range moduleEvents {
		if v := luaStr(t, ev); v != "" {
			m.Handlers[ev] = v
		}
	}
	return m
}

const storageTypeName = "atsume.Storage"

// pushStorage exposes a string map as Storage['key'] for reading and writing.
// A missing key reads as "" rather than nil, because templates compare it to ”.
func pushStorage(L *lua.LState, m map[string]string) lua.LValue {
	mt := L.GetTypeMetatable(storageTypeName)
	if mt == lua.LNil {
		mt = L.NewTypeMetatable(storageTypeName)
		L.SetField(mt, "__index", L.NewFunction(func(L *lua.LState) int {
			store := L.CheckUserData(1).Value.(map[string]string)
			L.Push(lua.LString(store[L.CheckString(2)]))
			return 1
		}))
		L.SetField(mt, "__newindex", L.NewFunction(func(L *lua.LState) int {
			store := L.CheckUserData(1).Value.(map[string]string)
			store[L.CheckString(2)] = L.CheckString(3)
			return 0
		}))
	}
	ud := L.NewUserData()
	ud.Value = m
	L.SetMetatable(ud, mt)
	return ud
}

func luaStr(t *lua.LTable, key string) string {
	v := t.RawGetString(key)
	if v == lua.LNil {
		return ""
	}
	return lua.LVAsString(v)
}

// optionValue converts a declared default to a plain Go value. A whole number
// becomes an int64, since that is what a spin edit or combo box index is.
func optionValue(v lua.LValue) any {
	switch v := v.(type) {
	case *lua.LNilType:
		return nil
	case lua.LBool:
		return bool(v)
	case lua.LNumber:
		if f := float64(v); f == math.Trunc(f) && math.Abs(f) < 1<<53 {
			return int64(f)
		}
		return float64(v)
	case lua.LString:
		return string(v)
	default:
		return v.String()
	}
}

// luaOptionValue is the inverse of optionValue, for handing a default back to
// a module through MODULE.GetOption.
func luaOptionValue(v any) lua.LValue {
	switch v := v.(type) {
	case bool:
		return lua.LBool(v)
	case int64:
		return lua.LNumber(v)
	case float64:
		return lua.LNumber(v)
	case string:
		return lua.LString(v)
	default:
		return lua.LNil
	}
}
