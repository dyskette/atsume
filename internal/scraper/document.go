package scraper

import (
	"fmt"

	rt "github.com/arnodel/golua/runtime"
)

// Document wraps a response body.
//
// FMD2's HTTP.Document is a TMemoryStream, and modules use it two ways: handed
// straight to CreateTXQuery (1089 call sites) and as HTTP.Document.ToString()
// (153). A plain Lua string would serve the first and break the second, so this
// is userdata that also renders as a string.
type Document struct{ data []byte }

// Set replaces the body. Modules rewrite HTTP.Document in place, for instance
// when descrambling a tiled image.
func (d *Document) Set(b []byte) {
	if d != nil {
		d.data = b
	}
}

// Bytes returns the body.
func (d *Document) Bytes() []byte {
	if d == nil {
		return nil
	}
	return d.data
}

// String returns the body as text.
func (d *Document) String() string {
	if d == nil {
		return ""
	}
	return string(d.data)
}

const documentTypeName = "atsume.Document"

// pushDocument wraps an existing Document. The same pointer is shared
// with Go, so a module rewriting it is visible to the caller afterwards.
func pushDocument(r *rt.Runtime, d *Document) rt.Value {
	meta := typeMeta(r, documentTypeName, func() *rt.Table {
		size := func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toDocument(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(d.Bytes())))), nil
		}
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toDocument(c)
			if err != nil {
				return nil, err
			}
			key, err := checkString(c, 1)
			if err != nil {
				return nil, err
			}
			var v rt.Value
			switch key {
			case "ToString":
				v = luaMethod(key, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
					return rt.StringValue(d.String()), nil
				})
			case "Size":
				v = rt.IntValue(int64(len(d.Bytes())))
			}
			return c.PushingNext1(t.Runtime, v), nil
		}, "__index", 2, false)))
		mt.Set(rt.StringValue("__tostring"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toDocument(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(d.String())), nil
		}, "__tostring", 1, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(size, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(d, meta))
}

func toDocument(c *rt.GoCont) (*Document, error) {
	if d := documentArg(c, 0); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("bad argument #1 (Document expected, got %s)", c.Arg(0).TypeName())
}

// documentArg returns the Document at argument n, or nil when it is not
// one.
func documentArg(c *rt.GoCont, n int) *Document {
	if u, ok := c.Arg(n).TryUserData(); ok {
		d, _ := u.Value().(*Document)
		return d
	}
	return nil
}

// argText reads a text argument that may arrive as a Lua string, a
// number, or a Document, which is how modules pass HTTP.Document around.
// Anything else reads as "".
func argText(c *rt.GoCont, n int) string {
	if d := documentArg(c, n); d != nil {
		return d.String()
	}
	return luaString(c.Arg(n))
}
