package scraper

import (
	"strings"

	lua "github.com/yuin/gopher-lua"
)

// Strings is the Go side of Pascal's TStringList, which FMD2 modules use for
// LINKS, NAMES, chapter lists, page lists, headers and cookies.
//
// Lua sees it as userdata supporting Add/Clear/Count/Reverse, 0-based indexing,
// and the Values['name'] form used for `name=value` pairs such as HTTP headers.
type Strings struct {
	items []string
}

// NewStrings returns an empty list.
func NewStrings() *Strings { return &Strings{} }

// Add appends a value.
func (s *Strings) Add(v string) { s.items = append(s.items, v) }

// Clear empties the list.
func (s *Strings) Clear() { s.items = nil }

// Count returns the number of items.
func (s *Strings) Count() int { return len(s.items) }

// All returns a copy of the items.
func (s *Strings) All() []string { return append([]string(nil), s.items...) }

// Set replaces the contents.
func (s *Strings) Set(v []string) { s.items = append([]string(nil), v...) }

// Reverse flips the order, which modules use to put chapters oldest-first.
func (s *Strings) Reverse() {
	for i, j := 0, len(s.items)-1; i < j; i, j = i+1, j-1 {
		s.items[i], s.items[j] = s.items[j], s.items[i]
	}
}

// Get returns the item at a 0-based index, or "" when out of range.
func (s *Strings) Get(i int) string {
	if i < 0 || i >= len(s.items) {
		return ""
	}
	return s.items[i]
}

// Put writes the item at a 0-based index, growing the list as needed.
func (s *Strings) Put(i int, v string) {
	if i < 0 {
		return
	}
	for len(s.items) <= i {
		s.items = append(s.items, "")
	}
	s.items[i] = v
}

// CommaText renders the list in Pascal's TStringList.CommaText form: values
// separated by commas, quoted when they contain a comma, a quote or a space.
func (s *Strings) CommaText() string {
	parts := make([]string, 0, len(s.items))
	for _, it := range s.items {
		if strings.ContainsAny(it, `, "`) {
			parts = append(parts, `"`+strings.ReplaceAll(it, `"`, `""`)+`"`)
		} else {
			parts = append(parts, it)
		}
	}
	return strings.Join(parts, ",")
}

// SetCommaText replaces the contents by parsing a CommaText value. Modules
// assign a comma-separated list of image URLs this way.
func (s *Strings) SetCommaText(v string) {
	s.items = nil
	var cur strings.Builder
	inQuote := false
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case c == '"' && inQuote && i+1 < len(v) && v[i+1] == '"':
			cur.WriteByte('"')
			i++
		case c == '"':
			inQuote = !inQuote
		case c == ',' && !inQuote:
			s.items = append(s.items, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	if cur.Len() > 0 || len(s.items) > 0 {
		s.items = append(s.items, cur.String())
	}
}

// Value reads the `name=value` pair with the given name.
func (s *Strings) Value(name string) string {
	for _, it := range s.items {
		if k, v, ok := strings.Cut(it, "="); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			return v
		}
	}
	return ""
}

// SetValue writes a `name=value` pair, replacing any existing one.
func (s *Strings) SetValue(name, value string) {
	for i, it := range s.items {
		if k, _, ok := strings.Cut(it, "="); ok && strings.EqualFold(strings.TrimSpace(k), name) {
			if value == "" {
				s.items = append(s.items[:i], s.items[i+1:]...)
			} else {
				s.items[i] = name + "=" + value
			}
			return
		}
	}
	if value != "" {
		s.items = append(s.items, name+"="+value)
	}
}

const stringsTypeName = "atsume.Strings"

func registerStrings(L *lua.LState) {
	mt := L.NewTypeMetatable(stringsTypeName)
	L.SetField(mt, "__index", L.NewFunction(stringsIndex))
	L.SetField(mt, "__newindex", L.NewFunction(stringsNewIndex))
	L.SetField(mt, "__len", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LNumber(checkStrings(L, 1).Count()))
		return 1
	}))
}

// pushStrings wraps a list as Lua userdata. The same *Strings is shared with Go,
// so a module writing to LINKS mutates the list the caller reads afterwards.
func pushStrings(L *lua.LState, s *Strings) lua.LValue {
	ud := L.NewUserData()
	ud.Value = s
	L.SetMetatable(ud, L.GetTypeMetatable(stringsTypeName))
	return ud
}

func checkStrings(L *lua.LState, n int) *Strings {
	ud, ok := L.Get(n).(*lua.LUserData)
	if !ok {
		L.ArgError(n, "Strings expected")
		return nil
	}
	s, ok := ud.Value.(*Strings)
	if !ok {
		L.ArgError(n, "Strings expected")
		return nil
	}
	return s
}

// valuesProxy exposes list.Values['name'] as a settable table-like object.
type valuesProxy struct{ s *Strings }

const valuesTypeName = "atsume.StringsValues"

func registerValues(L *lua.LState) {
	mt := L.NewTypeMetatable(valuesTypeName)
	L.SetField(mt, "__index", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckUserData(1).Value.(*valuesProxy)
		L.Push(lua.LString(p.s.Value(L.CheckString(2))))
		return 1
	}))
	L.SetField(mt, "__newindex", L.NewFunction(func(L *lua.LState) int {
		p := L.CheckUserData(1).Value.(*valuesProxy)
		p.s.SetValue(L.CheckString(2), L.CheckString(3))
		return 0
	}))
}

func stringsIndex(L *lua.LState) int {
	s := checkStrings(L, 1)
	switch key := L.Get(2).(type) {
	case lua.LNumber:
		L.Push(lua.LString(s.Get(int(key))))
		return 1
	case lua.LString:
		switch string(key) {
		case "Count":
			L.Push(lua.LNumber(s.Count()))
		case "Text":
			L.Push(lua.LString(strings.Join(s.items, "\r\n")))
		case "CommaText":
			L.Push(lua.LString(s.CommaText()))
		case "Values":
			ud := L.NewUserData()
			ud.Value = &valuesProxy{s: s}
			L.SetMetatable(ud, L.GetTypeMetatable(valuesTypeName))
			L.Push(ud)
		// Modules always use dot notation (LINKS.Add(x)), never colon, so the
		// receiver is captured here rather than read off the stack.
		case "Add":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				s.Add(L.CheckString(1))
				return 0
			}))
		case "Clear":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				s.Clear()
				return 0
			}))
		case "Reverse":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				s.Reverse()
				return 0
			}))
		case "Delete":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				i := L.CheckInt(1)
				if i >= 0 && i < len(s.items) {
					s.items = append(s.items[:i], s.items[i+1:]...)
				}
				return 0
			}))
		case "IndexOf":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				want := L.CheckString(1)
				for i, it := range s.items {
					if it == want {
						L.Push(lua.LNumber(i))
						return 1
					}
				}
				L.Push(lua.LNumber(-1))
				return 1
			}))
		case "Get":
			L.Push(L.NewFunction(func(L *lua.LState) int {
				L.Push(lua.LString(s.Get(L.CheckInt(1))))
				return 1
			}))
		default:
			L.Push(lua.LNil)
		}
		return 1
	}
	L.Push(lua.LNil)
	return 1
}

func stringsNewIndex(L *lua.LState) int {
	s := checkStrings(L, 1)
	switch key := L.Get(2).(type) {
	case lua.LNumber:
		s.Put(int(key), L.CheckString(3))
	case lua.LString:
		switch string(key) {
		case "CommaText":
			s.SetCommaText(L.CheckString(3))
		case "Text":
			s.Set(splitLines(L.CheckString(3)))
		}
	}
	return 0
}

func splitLines(v string) []string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	if v == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(v, "\n"), "\n")
}
