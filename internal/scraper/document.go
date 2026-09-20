package scraper

import lua "github.com/yuin/gopher-lua"

// Document wraps a response body.
//
// FMD2's HTTP.Document is a TMemoryStream, and modules use it two ways: handed
// straight to CreateTXQuery (1089 call sites) and as HTTP.Document.ToString()
// (153). A plain Lua string would serve the first and break the second, so this
// is userdata that also renders as a string.
type Document struct{ data []byte }

// String returns the body as text.
func (d *Document) String() string {
	if d == nil {
		return ""
	}
	return string(d.data)
}

const documentTypeName = "atsume.Document"

func registerDocument(L *lua.LState) {
	mt := L.NewTypeMetatable(documentTypeName)
	L.SetField(mt, "__index", L.NewFunction(func(L *lua.LState) int {
		d := L.CheckUserData(1).Value.(*Document)
		switch L.CheckString(2) {
		case "ToString":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				L.Push(lua.LString(d.String()))
				return 1
			}))
		case "Size":
			L.Push(lua.LNumber(len(d.data)))
		default:
			L.Push(lua.LNil)
		}
		return 1
	}))
	L.SetField(mt, "__tostring", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(L.CheckUserData(1).Value.(*Document).String()))
		return 1
	}))
	L.SetField(mt, "__len", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LNumber(len(L.CheckUserData(1).Value.(*Document).data)))
		return 1
	}))
}

func pushDocument(L *lua.LState, data []byte) lua.LValue {
	ud := L.NewUserData()
	ud.Value = &Document{data: data}
	L.SetMetatable(ud, L.GetTypeMetatable(documentTypeName))
	return ud
}

// argText reads a text argument that may arrive either as a Lua string or as a
// Document, which is how modules pass HTTP.Document around.
func argText(L *lua.LState, n int) string {
	switch v := L.Get(n).(type) {
	case lua.LString:
		return string(v)
	case *lua.LUserData:
		if d, ok := v.Value.(*Document); ok {
			return d.String()
		}
	case lua.LNumber:
		return v.String()
	}
	return ""
}
