package scraper

import lua "github.com/yuin/gopher-lua"

// fields describes a Lua-visible object whose properties are backed directly by
// Go variables. FMD2 modules read and write the injected objects as plain
// records (MANGAINFO.Title = ..., TASK.PageNumber, HTTP.MimeType), so binding
// pointers is both the shortest and the most faithful mapping.
type fields struct {
	typeName string
	str      map[string]*string
	num      map[string]*int
	boolean  map[string]*bool
	list     map[string]*Strings
	methods  map[string]lua.LGFunction
	// getter and setter handle properties that need computation rather than a
	// backing variable.
	getter map[string]func(L *lua.LState) lua.LValue
	setter map[string]func(L *lua.LState, v lua.LValue)
}

func newFields(typeName string) *fields {
	return &fields{
		typeName: typeName,
		str:      map[string]*string{},
		num:      map[string]*int{},
		boolean:  map[string]*bool{},
		list:     map[string]*Strings{},
		methods:  map[string]lua.LGFunction{},
		getter:   map[string]func(L *lua.LState) lua.LValue{},
		setter:   map[string]func(L *lua.LState, v lua.LValue){},
	}
}

// push registers the metatable if needed and returns userdata bound to f.
func (f *fields) push(L *lua.LState) lua.LValue {
	mt := L.GetTypeMetatable(f.typeName)
	if mt == lua.LNil {
		mt = L.NewTypeMetatable(f.typeName)
		L.SetField(mt, "__index", L.NewFunction(fieldsIndex))
		L.SetField(mt, "__newindex", L.NewFunction(fieldsNewIndex))
	}
	ud := L.NewUserData()
	ud.Value = f
	L.SetMetatable(ud, mt)
	return ud
}

func toFields(L *lua.LState, n int) *fields {
	ud, ok := L.Get(n).(*lua.LUserData)
	if !ok {
		L.ArgError(n, "object expected")
		return nil
	}
	f, ok := ud.Value.(*fields)
	if !ok {
		L.ArgError(n, "object expected")
		return nil
	}
	return f
}

func fieldsIndex(L *lua.LState) int {
	f := toFields(L, 1)
	key := L.CheckString(2)
	switch {
	case f.str[key] != nil:
		L.Push(lua.LString(*f.str[key]))
	case f.num[key] != nil:
		L.Push(lua.LNumber(*f.num[key]))
	case f.boolean[key] != nil:
		L.Push(lua.LBool(*f.boolean[key]))
	case f.list[key] != nil:
		L.Push(pushStrings(L, f.list[key]))
	case f.methods[key] != nil:
		L.Push(L.NewFunction(f.methods[key]))
	case f.getter[key] != nil:
		L.Push(f.getter[key](L))
	default:
		L.Push(lua.LNil)
	}
	return 1
}

func fieldsNewIndex(L *lua.LState) int {
	f := toFields(L, 1)
	key := L.CheckString(2)
	v := L.Get(3)
	switch {
	case f.str[key] != nil:
		*f.str[key] = lua.LVAsString(v)
	case f.num[key] != nil:
		*f.num[key] = int(lua.LVAsNumber(v))
	case f.boolean[key] != nil:
		*f.boolean[key] = lua.LVAsBool(v)
	case f.setter[key] != nil:
		f.setter[key](L, v)
	}
	return 0
}
