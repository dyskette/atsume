package scraper

import (
	"fmt"
	"strings"

	rt "github.com/arnodel/golua/runtime"
)

// This file is the golua counterpart of the bindings in strings.go. The list
// itself, Strings, is shared: only how Lua reaches it differs.

// pushGoluaStrings wraps a list as Lua userdata. The same *Strings is shared
// with Go, so a module writing to LINKS mutates the list the caller reads.
func pushGoluaStrings(r *rt.Runtime, s *Strings) rt.Value {
	meta := typeMeta(r, stringsTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(stringsIndexGolua, "__index", 2, false)))
		mt.Set(rt.StringValue("__newindex"), rt.FunctionValue(newGoFunc(stringsNewIndexGolua, "__newindex", 3, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			s, err := toGoluaStrings(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(s.Count()))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(s, meta))
}

func toGoluaStrings(c *rt.GoCont) (*Strings, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if s, ok := u.Value().(*Strings); ok {
			return s, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (Strings expected, got %s)", c.Arg(0).TypeName())
}

// checkInt reads argument n as an integer, truncating a float and accepting a
// numeric string, as gopher-lua's CheckInt did.
func checkInt(c *rt.GoCont, n int) (int, error) {
	if _, _, tp := rt.ToNumber(c.Arg(n)); tp == rt.NaN {
		return 0, fmt.Errorf("bad argument #%d (number expected, got %s)", n+1, c.Arg(n).TypeName())
	}
	return truncInt(c.Arg(n)), nil
}

func stringsIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	s, err := toGoluaStrings(c)
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
		v = pushGoluaValues(t.Runtime, s)
	case "Add":
		v = goluaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			item, err := checkString(c, 0)
			if err == nil {
				s.Add(item)
			}
			return rt.NilValue, err
		})
	case "Clear":
		v = goluaMethod(name, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			s.Clear()
			return rt.NilValue, nil
		})
	case "Reverse":
		v = goluaMethod(name, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			s.Reverse()
			return rt.NilValue, nil
		})
	case "Delete":
		v = goluaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			i, err := checkInt(c, 0)
			if err == nil && i >= 0 && i < len(s.items) {
				s.items = append(s.items[:i], s.items[i+1:]...)
			}
			return rt.NilValue, err
		})
	case "IndexOf":
		v = goluaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
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
		v = goluaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			i, err := checkInt(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			return rt.StringValue(s.Get(i)), nil
		})
	}
	return c.PushingNext1(t.Runtime, v), nil
}

func stringsNewIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	s, err := toGoluaStrings(c)
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

// pushGoluaValues exposes list.Values['name'] for the name=value pairs such
// as HTTP headers.
func pushGoluaValues(r *rt.Runtime, s *Strings) rt.Value {
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
