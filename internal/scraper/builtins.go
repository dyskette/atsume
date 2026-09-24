package scraper

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	rt "github.com/arnodel/golua/runtime"
	"github.com/dyskette/atsume/internal/txquery"
)

// Handler return codes, injected as globals because every module returns them.
const (
	noError             = 0
	netProblem          = 1
	informationNotFound = 2
)

// Account states used by the 28 modules implementing OnLogin.
const (
	asUnknown  = -1
	asChecking = 0
	asValid    = 1
	asInvalid  = 2
)

const queryTypeName = "atsume.Query"

const nodeListTypeName = "atsume.NodeList"

const nodeTypeName = "atsume.Node"

// GetBetween returns the text between the first left and the following right.
func GetBetween(left, right, s string) string {
	i := strings.Index(s, left)
	if i < 0 {
		return ""
	}
	i += len(left)
	j := strings.Index(s[i:], right)
	if j < 0 {
		return ""
	}
	return s[i : i+j]
}

// NormaliseLink puts a link into the shape FMD2 stores, which is the shape
// modules are written to expect.
//
// Upstream runs every link a module produces through RemoveHostFromURL,
// whose last act is `if iurl <> ” then ipath := '/' + iurl`. So a module
// that hands back a bare identifier gets it back with a leading slash, and
// MangaDex's page fetch — API_URL .. '/at-home/server' .. URL — resolves.
// Without it the two run together and the request 404s, which surfaced as
// "module returned no pages" with nothing to say why.
//
// Upstream also strips the host. atsume does not: every fetch here goes
// through MaybeFillHost, which passes an absolute address through unchanged,
// and only two modules in the catalogue concatenate onto MODULE.RootURL
// where it would matter. Stripping it would rewrite links already stored
// against series that are already followed, for no gain.
func NormaliseLink(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if strings.HasPrefix(link, "/") || strings.Contains(link, "://") {
		return link
	}
	return "/" + link
}

// MaybeFillHost prefixes a root URL onto a path that lacks a host.
func MaybeFillHost(root, u string) string {
	if u == "" {
		return root
	}
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	base, err := url.Parse(root)
	if err != nil {
		return u
	}
	ref, err := url.Parse(u)
	if err != nil {
		return u
	}
	return base.ResolveReference(ref).String()
}

// Default keyword lists for MangaInfoStatusIfPos, matching uBaseUnit.pas.
//
// Modules call it with one argument and rely on these; without them a site that
// says "Completed" records no status at all.
const (
	defaultOngoing   = "ongoing"
	defaultCompleted = "complete"
	defaultHiatus    = "hiatus"
	defaultDropped   = "cancel"
)

// MangaInfoStatusIfPos maps a site's status text onto a fixed vocabulary. Each
// argument is a pipe-separated list of substrings to look for.
//
// The order is ongoing, completed, hiatus, dropped — the same as upstream,
// which matters when a status contains keywords from more than one list.
// Upstream returns numeric codes and the literal "Unknown"; readable strings
// are used here instead, with "" for no match so the interface can simply omit
// the badge.
func MangaInfoStatusIfPos(s, ongoing, completed, hiatus, dropped string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	lower := strings.ToLower(s)
	for _, c := range []struct{ list, out string }{
		{ongoing, "ongoing"},
		{completed, "completed"},
		{hiatus, "hiatus"},
		{dropped, "dropped"},
	} {
		for _, kw := range strings.Split(c.list, "|") {
			if kw = strings.TrimSpace(kw); kw != "" && strings.Contains(lower, strings.ToLower(kw)) {
				return c.out
			}
		}
	}
	return ""
}

// maxSleep caps what a module can ask to wait for. The longest deliberate
// pause upstream uses is a few seconds.
const maxSleep = 30 * time.Second

// queryHolder holds the parsed document behind a query object. It is a holder
// rather than the *txquery.Query itself because ParseHTML replaces the
// document in place, and golua userdata cannot change its value.
type queryHolder struct{ q *txquery.Query }

// nodeList is what XPath() returns. Modules consume it either as
// list.Get() in a generic for, or by index through Get(i).
type nodeList struct{ nodes []*txquery.Node }

func pushQuery(r *rt.Runtime, q *txquery.Query) rt.Value {
	meta := typeMeta(r, queryTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(queryIndex, "__index", 2, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(&queryHolder{q: q}, meta))
}

func pushNodeList(r *rt.Runtime, nodes []*txquery.Node) rt.Value {
	meta := typeMeta(r, nodeListTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(nodeListIndex, "__index", 2, false)))
		mt.Set(rt.StringValue("__len"), rt.FunctionValue(newGoFunc(func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			nl, err := toNodeList(c)
			if err != nil {
				return nil, err
			}
			return c.PushingNext1(t.Runtime, rt.IntValue(int64(len(nl.nodes)))), nil
		}, "__len", 1, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(&nodeList{nodes: nodes}, meta))
}

func pushNode(r *rt.Runtime, n *txquery.Node) rt.Value {
	meta := typeMeta(r, nodeTypeName, func() *rt.Table {
		mt := rt.NewTable()
		mt.Set(rt.StringValue("__index"), rt.FunctionValue(newGoFunc(nodeIndex, "__index", 2, false)))
		return mt
	})
	return rt.UserDataValue(rt.NewUserData(n, meta))
}

func toNodeList(c *rt.GoCont) (*nodeList, error) {
	if u, ok := c.Arg(0).TryUserData(); ok {
		if nl, ok := u.Value().(*nodeList); ok {
			return nl, nil
		}
	}
	return nil, fmt.Errorf("bad argument #1 (node list expected, got %s)", c.Arg(0).TypeName())
}

// at returns the node at a 1-based index, as upstream's Get(i) counts, or
// nil when out of range.
func (nl *nodeList) at(r *rt.Runtime, i int) rt.Value {
	if i < 1 || i > len(nl.nodes) {
		return rt.NilValue
	}
	return pushNode(r, nl.nodes[i-1])
}

func nodeListIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	nl, err := toNodeList(c)
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

// contextNode resolves the optional context argument a module passes to
// XPathString and friends: a node, or a node list meaning its first node.
func contextNode(c *rt.GoCont, n int) *txquery.Node {
	u, ok := c.Arg(n).TryUserData()
	if !ok {
		return nil
	}
	switch v := u.Value().(type) {
	case *txquery.Node:
		return v
	case *nodeList:
		if len(v.nodes) > 0 {
			return v.nodes[0]
		}
	}
	return nil
}

// stringsArg returns the Strings list at argument n, or nil when it is
// not one.
func stringsArg(c *rt.GoCont, n int) *Strings {
	if u, ok := c.Arg(n).TryUserData(); ok {
		s, _ := u.Value().(*Strings)
		return s
	}
	return nil
}

// appendAll adds values to a Strings argument, skipping a missing one so
// a module may pass only the list it cares about.
func appendAll(c *rt.GoCont, n int, vals []string) {
	if s := stringsArg(c, n); s != nil {
		for _, v := range vals {
			s.Add(v)
		}
	}
}

func queryIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
	u, ok := c.Arg(0).TryUserData()
	var h *queryHolder
	if ok {
		h, ok = u.Value().(*queryHolder)
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
		v = luaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := contextNode(c, 1); ctx != nil {
				return rt.StringValue(ctx.XPathString(expr)), nil
			}
			return rt.StringValue(h.q.XPathString(expr)), nil
		})
	case "XPathStringAll":
		v = luaMethod(key, 3, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			// Upstream overloads this three ways: a separator, an output list
			// to fill, or a context node. Madara uses the list form to collect
			// TASK.PageLinks, so all three have to be distinguished here.
			if out := stringsArg(c, 1); out != nil {
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
			if ctx := contextNode(c, ctxArg); ctx != nil {
				return rt.StringValue(ctx.XPathStringAll(expr, sep)), nil
			}
			return rt.StringValue(h.q.XPathStringAll(expr, sep)), nil
		})
	case "XPathCount":
		v = luaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := contextNode(c, 1); ctx != nil {
				return rt.IntValue(int64(ctx.XPathCount(expr))), nil
			}
			return rt.IntValue(int64(h.q.XPathCount(expr))), nil
		})
	case "XPath":
		v = luaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			if ctx := contextNode(c, 1); ctx != nil {
				return pushNodeList(t.Runtime, ctx.XPath(expr)), nil
			}
			return pushNodeList(t.Runtime, h.q.XPath(expr)), nil
		})
	case "XPathHREFAll", "XPathHREFTitleAll":
		// XPathHREFAll(expr, links, names[, contextNode]); the context node
		// scopes the search.
		title := key == "XPathHREFTitleAll"
		v = luaMethod(key, 4, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			expr, err := checkString(c, 0)
			if err != nil {
				return rt.NilValue, err
			}
			var links, names []string
			switch ctx := contextNode(c, 3); {
			case ctx != nil && title:
				links, names = ctx.XPathHREFTitleAll(expr)
			case ctx != nil:
				links, names = ctx.XPathHREFAll(expr)
			case title:
				links, names = h.q.XPathHREFTitleAll(expr)
			default:
				links, names = h.q.XPathHREFAll(expr)
			}
			appendAll(c, 1, links)
			appendAll(c, 2, names)
			return rt.NilValue, nil
		})
	case "ParseHTML":
		v = luaMethod(key, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			// Reparsing replaces the document in place, matching x.ParseHTML(s).
			if nq, err := txquery.ParseString(argText(c, 0)); err == nil {
				h.q = nq
			}
			return rt.NilValue, nil
		})
	}
	return c.PushingNext1(t.Runtime, v), nil
}

func nodeIndex(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
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
		return luaMethod(name, 1, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
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
		v = luaMethod(key, 0, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
			return rt.StringValue(n.Text()), nil
		})
	case "GetAttribute":
		v = str1(key, func(s string) rt.Value { return rt.StringValue(n.Attribute(s)) })
	case "GetProperty":
		// Upstream returns a value object here, and modules chain straight
		// into .ToString(), so this must be a node rather than a string.
		v = str1(key, func(s string) rt.Value { return pushNode(t.Runtime, n.Property(s)) })
	case "XPathString":
		v = str1(key, func(s string) rt.Value { return rt.StringValue(n.XPathString(s)) })
	case "XPathStringAll":
		v = luaMethod(key, 2, func(t *rt.Thread, c *rt.GoCont) (rt.Value, error) {
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
		v = str1(key, func(s string) rt.Value { return pushNodeList(t.Runtime, n.XPath(s)) })
	}
	return c.PushingNext1(t.Runtime, v), nil
}

// registerBuiltins installs the free functions and constants modules expect.
// ctx bounds sleep.
func registerBuiltins(r *rt.Runtime, ctx context.Context) {
	env := r.GlobalEnv()
	for name, v := range map[string]int64{
		"no_error":              noError,
		"net_problem":           netProblem,
		"information_not_found": informationNotFound,
		"asUnknown":             asUnknown,
		"asChecking":            asChecking,
		"asValid":               asValid,
		"asInvalid":             asInvalid,
	} {
		env.Set(rt.StringValue(name), rt.IntValue(v))
	}

	// sleep(milliseconds) is an upstream global. Twelve modules use it, and
	// they are not being polite: a site that drip-feeds images over repeated
	// requests gives back only a couple per request without a pause between
	// them, so skipping the wait loses pages.
	//
	// It is capped at maxSleep, because a module asking to sleep for an hour
	// has gone wrong. A cancelled context ends the whole call rather than
	// just the wait, since the module would otherwise carry on as though it
	// had slept.
	setGoFunc(r, env, "sleep", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		ms, err := checkInt(c, 0)
		if err != nil {
			return nil, err
		}
		d := time.Duration(ms) * time.Millisecond
		if d <= 0 {
			return c.Next(), nil
		}
		d = min(d, maxSleep)
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.TerminateContext("sleep: %v", ctx.Err())
		}
		return c.Next(), nil
	}, 1, false)

	setGoFunc(r, env, "CreateTXQuery", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		// The argument is usually HTTP.Document, which is userdata.
		q, err := txquery.ParseString(argText(c, 0))
		if err != nil {
			return c.PushingNext1(t.Runtime, rt.NilValue), nil
		}
		return c.PushingNext1(t.Runtime, pushQuery(t.Runtime, q)), nil
	}, 1, false)

	// strFns adapts functions of plain strings, all arguments required.
	strFns := map[string]struct {
		n  int
		fn func(a []string) string
	}{
		"GetBetween": {3, func(a []string) string { return GetBetween(a[0], a[1], a[2]) }},
		"SeparateLeft": {2, func(a []string) string {
			left, _, _ := strings.Cut(a[0], a[1])
			return left
		}},
		"SeparateRight": {2, func(a []string) string {
			if _, right, ok := strings.Cut(a[0], a[1]); ok {
				return right
			}
			return a[0]
		}},
		"Trim":          {1, func(a []string) string { return strings.TrimSpace(a[0]) }},
		"MaybeFillHost": {2, func(a []string) string { return MaybeFillHost(a[0], a[1]) }},
	}
	for name, f := range strFns {
		setGoFunc(r, env, name, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			args := make([]string, f.n)
			for i := range args {
				s, err := checkString(c, i)
				if err != nil {
					return nil, err
				}
				args[i] = s
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(f.fn(args))), nil
		}, f.n, false)
	}

	setGoFunc(r, env, "MangaInfoStatusIfPos", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		lists := [4]string{defaultOngoing, defaultCompleted, defaultHiatus, defaultDropped}
		for i := range lists {
			if lists[i], err = optString(c, i+1, lists[i]); err != nil {
				return nil, err
			}
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(
			MangaInfoStatusIfPos(s, lists[0], lists[1], lists[2], lists[3]))), nil
	}, 5, false)
}
