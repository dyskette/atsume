package scraper

import (
	"fmt"
	"strings"

	rt "github.com/arnodel/golua/runtime"
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

// valuesProxy exposes list.Values['name'] as a settable table-like object.
type valuesProxy struct{ s *Strings }

const valuesTypeName = "atsume.StringsValues"

func splitLines(v string) []string {
	v = strings.ReplaceAll(v, "\r\n", "\n")
	if v == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(v, "\n"), "\n")
}

// pushStrings wraps a list as Lua userdata. The same *Strings is shared
// with Go, so a module writing to LINKS mutates the list the caller reads.
func pushStrings(r *rt.Runtime, s *Strings) rt.Value {
	meta := typeMeta(r, stringsTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(stringsIndex, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(stringsNewIndex, "__newindex", 3, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			s, err := toStrings(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(s.Count()))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(s, meta))
}

func toStrings(c *rt.GoCont) (*Strings, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if s, ok := u.Value().(*Strings); ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (Strings expected, got %s)", c.Arg(0).TypeName())
}

// checkInt reads argument n as an integer, truncating a float and accepting a
// numeric string.
func checkInt(c *rt.GoCont, n int) (int, error) {
	if _, _, tp := rt.ToNumber(c.Arg(n)); tp == rt.NaN {
		return 0, fmt.Errorf("bad argument #%d (number expected, got %s)", n+1, c.Arg(n).TypeName())
	}
	return truncInt(c.Arg(n)), nil
}

func stringsIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	s, err := toStrings(c)
	if err != nil {
		return nil, err
	}
	key := c.Arg(1)
	if key.Type() == rt.IntType || key.Type() == rt.FloatType {
		return c.PushingNext1(t.Runtime, rt.StringValue(s.Get(truncInt(key)))), nil
	}
	name, ok := key.TryString()
	if !ok {
		return c.PushingNext1(t.Runtime, rt.NilValue), nil
	}

	var v rt.Value
	switch name {
	case "Count":
		v = rt.IntValue(int64(s.Count()))
	case "Text":
		v = rt.StringValue(strings.Join(s.items, "\r\n"))
	case "CommaText":
		v = rt.StringValue(s.CommaText())
	case "Values":
		v = pushValues(t.Runtime, s)
	case "Add":
		v = luaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			item, err := checkString(c, 0)
			if err == nil {
				s.Add(item)
			}
			return rt.NilValue, err
		})
	case "Clear":
		v = luaMethod(name, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			s.Clear()
			return rt.NilValue, nil
		})
	case "Reverse":
		v = luaMethod(name, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			s.Reverse()
			return rt.NilValue, nil
		})
	case "Delete":
		v = luaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			i, err := checkInt(c, 0)
			if err == nil && i >= 0 && i < len(s.items) {
				s.items = append(s.items[:i], s.items[i+1:]...)
			}
			return rt.NilValue, err
		})
	case "IndexOf":
		v = luaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			want, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			for i, it := range s.items {
				if it == want {
					return rt.IntValue(int64(i)), nil
				}
			}
			return rt.IntValue(-1), nil
		})
	case "Get":
		v = luaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			i, err := checkInt(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			return rt.StringValue(s.Get(i)), nil
		})
	}
	return c.PushingNext1(t.Runtime, v), nil
}

func stringsNewIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	s, err := toStrings(c)
	if err != nil {
		return nil, err
	}
	key := c.Arg(1)
	switch key.Type() {
	case rt.IntType, rt.FloatType:
		v, err := checkString(c, 2)
		if err != nil {
			return nil, err
		}
		s.Put(truncInt(key), v)
	case rt.StringType:
		switch key.AsString() {
		case "CommaText":
			v, err := checkString(c, 2)
			if err != nil {
				return nil, err
			}
			s.SetCommaText(v)
		case "Text":
			v, err := checkString(c, 2)
			if err != nil {
				return nil, err
			}
			s.Set(splitLines(v))
		}
	}
	return c.Next(), nil
}

// pushValues exposes list.Values['name'] for the name=value pairs such
// as HTTP headers.
func pushValues(r *rt.Runtime, s *Strings) rt.Value {
	meta := typeMeta(r, valuesTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toValuesProxy(c)
			if err != nil {
				return nil, err
			}
			name, err := checkString(c, 1)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(p.s.Value(name))), nil
		}, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			p, err := toValuesProxy(c)
			if err != nil {
				return nil, err
			}
			name, err := checkString(c, 1)
			if err != nil {
				return nil, err
			}
			value, err := checkString(c, 2)
			if err != nil {
				return nil, err
			}
			p.s.SetValue(name, value)
			return c.Next(), nil
		}, "__newindex", 3, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(&valuesProxy{s: s}, meta))
}

func toValuesProxy(c *rt.GoCont) (*valuesProxy, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if p, ok := u.Value().(*valuesProxy); ok {
			return p, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (Values expected, got %s)", c.Arg(0).TypeName())
}
