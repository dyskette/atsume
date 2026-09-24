package scraper

import (
	"fmt"

	rt "github.com/arnodel/golua/runtime"
	"github.com/dyskette/atsume/internal/txquery"
)

// This file is the golua counterpart of the Document binding in document.go
// and the query, node and node list bindings in builtins.go.

// pushGoluaDocument wraps an existing Document. The same pointer is shared
// with Go, so a module rewriting it is visible to the caller afterwards.
func pushGoluaDocument(r *rt.Runtime, d *Document) rt.Value {
	meta := typeMeta(r, documentTypeName, func() *rt.Table {
		size := func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toGoluaDocument(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(d.Bytes())))), nil
		}
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toGoluaDocument(c)
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
				v = goluaMethod(key, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
					return rt.StringValue(d.String()), nil
				})
			case "Size":
				v = rt.IntValue(int64(len(d.Bytes())))
			}
			return c.PushingNext1(t.Runtime, v), nil
		}, "__index", 2, false)))
		mt.Set(rt.StringValue("__tostring"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			d, err := toGoluaDocument(c)
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

func toGoluaDocument(c *rt.GoCont) (*Document, error) {
	if d := goluaDocumentArg(c, 0); d != nil {
		return d, nil
	}
	return nil, fmt.Errorf("bad argument #1 (Document expected, got %s)", c.Arg(0).TypeName())
}

// goluaDocumentArg returns the Document at argument n, or nil when it is not
// one.
func goluaDocumentArg(c *rt.GoCont, n int) *Document {
	if u, ok := c.Arg(n).TryUserData(); ok {
		d, _ := u.Value().(*Document)
		return d
	}
	return nil
}

// goluaArgText reads a text argument that may arrive as a Lua string, a
// number, or a Document, which is how modules pass HTTP.Document around.
// Anything else reads as "".
func goluaArgText(c *rt.GoCont, n int) string {
	if d := goluaDocumentArg(c, n); d != nil {
		return d.String()
	}
	return luaString(c.Arg(n))
}

// goluaQuery holds the parsed document behind a query object. It is a holder
// rather than the *txquery.Query itself because ParseHTML replaces the
// document in place, and golua userdata cannot change its value.
type goluaQuery struct{ q *txquery.Query }

// goluaNodeList is what XPath() returns. Modules consume it either as
// list.Get() in a generic for, or by index through Get(i).
type goluaNodeList struct{ nodes []*txquery.Node }

func pushGoluaQuery(r *rt.Runtime, q *txquery.Query) rt.Value {
	meta := typeMeta(r, queryTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(queryIndexGolua, "__index", 2, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(&goluaQuery{q: q}, meta))
}

func pushGoluaNodeList(r *rt.Runtime, nodes []*txquery.Node) rt.Value {
	meta := typeMeta(r, nodeListTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(nodeListIndexGolua, "__index", 2, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			nl, err := toGoluaNodeList(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(nl.nodes)))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(&goluaNodeList{nodes: nodes}, meta))
}

func pushGoluaNode(r *rt.Runtime, n *txquery.Node) rt.Value {
	meta := typeMeta(r, nodeTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(nodeIndexGolua, "__index", 2, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(n, meta))
}

func toGoluaNodeList(c *rt.GoCont) (*goluaNodeList, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if nl, ok := u.Value().(*goluaNodeList); ok {
			return nl, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (node list expected, got %s)", c.Arg(0).TypeName())
}

// at returns the node at a 1-based index, as upstream's Get(i) counts, or
// nil when out of range.
func (nl *goluaNodeList) at(r *rt.Runtime, i int) rt.Value {
	if i < 1 || i > len(nl.nodes) {
		return rt.NilValue
	}
	return pushGoluaNode(r, nl.nodes[i-1])
}

func nodeListIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	nl, err := toGoluaNodeList(c)
	if err != nil {
		return nil, err
	}
	key := c.Arg(1)
	if key.Type() == rt.IntType || key.Type() == rt.FloatType {
		return c.PushingNext1(t.Runtime, nl.at(t.Runtime, truncInt(key))), nil
	}
	var v rt.Value
	switch name, _ := key.TryString(); name {
	case "Count":
		v = rt.IntValue(int64(len(nl.nodes)))
	case "Get":
		// Get() with no argument yields an iterator; Get(i) returns one node.
		v = rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			if c.NArgs() >= 1 {
				i, err := checkInt(c, 0)
				if err != nil {
					return nil, err
				}
				return c.PushingNext1(t.Runtime, nl.at(t.Runtime, i)), nil
			}
			i := 0
			next := newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
				i++
				return c.PushingNext1(t.Runtime, nl.at(t.Runtime, i)), nil
			}, "Get", 0, false)
			return c.PushingNext1(t.Runtime, rt.FunctionValue(next)), nil
		}, "Get", 1, false))
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// goluaContextNode resolves the optional context argument a module passes to
// XPathString and friends: a node, or a node list meaning its first node.
func goluaContextNode(c *rt.GoCont, n int) *txquery.Node {
	u, ok := c.Arg(n).TryUserData()
	if !ok {
		return nil
	}
	switch v := u.Value().(type) {
	case *txquery.Node:
		return v
	case *goluaNodeList:
		if len(v.nodes) > 0 {
			return v.nodes[0]
		}
	}
	return nil
}

// goluaStringsArg returns the Strings list at argument n, or nil when it is
// not one.
func goluaStringsArg(c *rt.GoCont, n int) *Strings {
	if u, ok := c.Arg(n).TryUserData(); ok {
		s, _ := u.Value().(*Strings)
		return s
	}
	return nil
}

// goluaAppendAll adds values to a Strings argument, skipping a missing one so
// a module may pass only the list it cares about.
func goluaAppendAll(c *rt.GoCont, n int, vals []string) {
	if s := goluaStringsArg(c, n); s != nil {
		for _, v := range vals {
			s.Add(v)
		}
	}
}

func queryIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	u, ok := c.Arg(0).TryUserData()
	var h *goluaQuery
	if ok {
		h, ok = u.Value().(*goluaQuery)
	}
	if !ok {
		return nil, fmt.Errorf("bad argument #1 (query expected, got %s)", c.Arg(0).TypeName())
	}
	key, err := checkString(c, 1)
	if err != nil {
		return nil, err
	}

	var v rt.Value
	switch key {
	case "XPathString":
		// x.XPathString(expr) or x.XPathString(expr, contextNode)
		v = goluaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := goluaContextNode(c, 1); ctx != nil {
				return rt.StringValue(ctx.XPathString(expr)), nil
			}
			return rt.StringValue(h.q.XPathString(expr)), nil
		})
	case "XPathStringAll":
		v = goluaMethod(key, 3, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			// Upstream overloads this three ways: a separator, an output list
			// to fill, or a context node. Madara uses the list form to collect
			// TASK.PageLinks, so all three have to be distinguished here.
			if out := goluaStringsArg(c, 1); out != nil {
				for _, v := range h.q.XPathValues(expr, nil) {
					if v != "" {
						out.Add(v)
					}
				}
				return rt.NilValue, nil
			}
			sep, ctxArg := txquery.DefaultSeparator, 1
			if s, ok := c.Arg(1).TryString(); ok {
				sep, ctxArg = s, 2
			}
			if ctx := goluaContextNode(c, ctxArg); ctx != nil {
				return rt.StringValue(ctx.XPathStringAll(expr, sep)), nil
			}
			return rt.StringValue(h.q.XPathStringAll(expr, sep)), nil
		})
	case "XPathCount":
		v = goluaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := goluaContextNode(c, 1); ctx != nil {
				return rt.IntValue(int64(ctx.XPathCount(expr))), nil
			}
			return rt.IntValue(int64(h.q.XPathCount(expr))), nil
		})
	case "XPath":
		v = goluaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := goluaContextNode(c, 1); ctx != nil {
				return pushGoluaNodeList(t.Runtime, ctx.XPath(expr)), nil
			}
			return pushGoluaNodeList(t.Runtime, h.q.XPath(expr)), nil
		})
	case "XPathHREFAll", "XPathHREFTitleAll":
		// XPathHREFAll(expr, links, names[, contextNode]); the context node
		// scopes the search.
		title := key == "XPathHREFTitleAll"
		v = goluaMethod(key, 4, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			var links, names []string
			switch ctx := goluaContextNode(c, 3); {
			case ctx != nil && title:
				links, names = ctx.XPathHREFTitleAll(expr)
			case ctx != nil:
				links, names = ctx.XPathHREFAll(expr)
			case title:
				links, names = h.q.XPathHREFTitleAll(expr)
			default:
				links, names = h.q.XPathHREFAll(expr)
			}
			goluaAppendAll(c, 1, links)
			goluaAppendAll(c, 2, names)
			return rt.NilValue, nil
		})
	case "ParseHTML":
		v = goluaMethod(key, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			// Reparsing replaces the document in place, matching x.ParseHTML(s).
			if nq, err := txquery.ParseString(goluaArgText(c, 0)); err == nil {
				h.q = nq
			}
			return rt.NilValue, nil
		})
	}
	return c.PushingNext1(t.Runtime, v), nil
}

func nodeIndexGolua(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	u, ok := c.Arg(0).TryUserData()
	var n *txquery.Node
	if ok {
		n, ok = u.Value().(*txquery.Node)
	}
	if !ok {
		return nil, fmt.Errorf("bad argument #1 (node expected, got %s)", c.Arg(0).TypeName())
	}
	key, err := checkString(c, 1)
	if err != nil {
		return nil, err
	}

	// str1 adapts a node method taking one string.
	str1 := func(name string, fn func(string) rt.Value) rt.Value {
		return goluaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			s, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			return fn(s), nil
		})
	}
	var v rt.Value
	switch key {
	case "ToString":
		v = goluaMethod(key, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			return rt.StringValue(n.Text()), nil
		})
	case "GetAttribute":
		v = str1(key, func(s string) rt.Value { return rt.StringValue(n.Attribute(s)) })
	case "GetProperty":
		// Upstream returns a value object here, and modules chain straight
		// into .ToString(), so this must be a node rather than a string.
		v = str1(key, func(s string) rt.Value { return pushGoluaNode(t.Runtime, n.Property(s)) })
	case "XPathString":
		v = str1(key, func(s string) rt.Value { return rt.StringValue(n.XPathString(s)) })
	case "XPathStringAll":
		v = goluaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			sep, err := optString(c, 1, txquery.DefaultSeparator)
			if err != nil {
				return rt.NilValue, err
			}
			return rt.StringValue(n.XPathStringAll(expr, sep)), nil
		})
	case "XPath":
		v = str1(key, func(s string) rt.Value { return pushGoluaNodeList(t.Runtime, n.XPath(s)) })
	}
	return c.PushingNext1(t.Runtime, v), nil
}
