package scraper

import (
	"net/url"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/txquery"
	lua "github.com/yuin/gopher-lua"
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

// registerQuery installs the object CreateTXQuery returns.
func registerQuery(L *lua.LState) {
	mt := L.NewTypeMetatable(queryTypeName)
	L.SetField(mt, "__index", L.NewFunction(queryIndex))
}

func pushQuery(L *lua.LState, q *txquery.Query) lua.LValue {
	ud := L.NewUserData()
	ud.Value = q
	L.SetMetatable(ud, L.GetTypeMetatable(queryTypeName))
	return ud
}

const nodeListTypeName = "atsume.NodeList"

func registerNodeList(L *lua.LState) {
	mt := L.NewTypeMetatable(nodeListTypeName)
	L.SetField(mt, "__index", L.NewFunction(nodeListIndex))
	L.SetField(mt, "__len", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LNumber(len(L.CheckUserData(1).Value.(*nodeList).nodes)))
		return 1
	}))
}

// nodeList is what XPath() returns. FMD2 modules consume it either as
// `list.Get()` in a generic for, or by index through Get(i).
type nodeList struct{ nodes []*txquery.Node }

func pushNodeList(L *lua.LState, nodes []*txquery.Node) lua.LValue {
	ud := L.NewUserData()
	ud.Value = &nodeList{nodes: nodes}
	L.SetMetatable(ud, L.GetTypeMetatable(nodeListTypeName))
	return ud
}

func nodeListIndex(L *lua.LState) int {
	nl := L.CheckUserData(1).Value.(*nodeList)
	switch key := L.Get(2).(type) {
	case lua.LNumber:
		i := int(key)
		if i >= 1 && i <= len(nl.nodes) { // Get(i) is 1-based upstream
			L.Push(pushNode(L, nl.nodes[i-1]))
		} else {
			L.Push(lua.LNil)
		}
		return 1
	case lua.LString:
		switch string(key) {
		case "Count":
			L.Push(lua.LNumber(len(nl.nodes)))
		case "Get":
			// Get() with no argument yields an iterator; Get(i) returns one node.
			L.Push(L.NewFunction(func(L *lua.LState) int {
				if L.GetTop() >= 1 {
					i := L.CheckInt(1)
					if i >= 1 && i <= len(nl.nodes) {
						L.Push(pushNode(L, nl.nodes[i-1]))
					} else {
						L.Push(lua.LNil)
					}
					return 1
				}
				i := 0
				L.Push(L.NewFunction(func(L *lua.LState) int {
					if i >= len(nl.nodes) {
						L.Push(lua.LNil)
						return 1
					}
					L.Push(pushNode(L, nl.nodes[i]))
					i++
					return 1
				}))
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

const nodeTypeName = "atsume.Node"

func registerNode(L *lua.LState) {
	mt := L.NewTypeMetatable(nodeTypeName)
	L.SetField(mt, "__index", L.NewFunction(nodeIndex))
}

func pushNode(L *lua.LState, n *txquery.Node) lua.LValue {
	ud := L.NewUserData()
	ud.Value = n
	L.SetMetatable(ud, L.GetTypeMetatable(nodeTypeName))
	return ud
}

func queryIndex(L *lua.LState) int {
	ud := L.CheckUserData(1)
	q := ud.Value.(*txquery.Query)
	switch L.CheckString(2) {
	case "XPathString":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			// x.XPathString(expr) or x.XPathString(expr, contextNode)
			if ctx := contextNode(L, 2); ctx != nil {
				L.Push(lua.LString(ctx.XPathString(L.CheckString(1))))
				return 1
			}
			L.Push(lua.LString(q.XPathString(L.CheckString(1))))
			return 1
		}))
	case "XPathStringAll":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			expr := L.CheckString(1)
			// Upstream overloads this three ways: a separator, an output list
			// to fill, or a context node. Madara uses the list form to collect
			// TASK.PageLinks, so all three have to be distinguished here.
			if out := stringsArg(L, 2); out != nil {
				for _, v := range q.XPathValues(expr, nil) {
					if v != "" {
						out.Add(v)
					}
				}
				return 0
			}
			sep, ctxArg := txquery.DefaultSeparator, 2
			if s, ok := L.Get(2).(lua.LString); ok {
				sep, ctxArg = string(s), 3
			}
			if ctx := contextNode(L, ctxArg); ctx != nil {
				L.Push(lua.LString(ctx.XPathStringAll(expr, sep)))
				return 1
			}
			L.Push(lua.LString(q.XPathStringAll(expr, sep)))
			return 1
		}))
	case "XPathCount":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			if ctx := contextNode(L, 2); ctx != nil {
				L.Push(lua.LNumber(ctx.XPathCount(L.CheckString(1))))
				return 1
			}
			L.Push(lua.LNumber(q.XPathCount(L.CheckString(1))))
			return 1
		}))
	case "XPath":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			if ctx := contextNode(L, 2); ctx != nil {
				L.Push(pushNodeList(L, ctx.XPath(L.CheckString(1))))
				return 1
			}
			L.Push(pushNodeList(L, q.XPath(L.CheckString(1))))
			return 1
		}))
	case "XPathHREFAll":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			// The fourth argument, when present, scopes the search to a node.
			var links, names []string
			if ctx := contextNode(L, 4); ctx != nil {
				links, names = ctx.XPathHREFAll(L.CheckString(1))
			} else {
				links, names = q.XPathHREFAll(L.CheckString(1))
			}
			appendAll(L, 2, links)
			appendAll(L, 3, names)
			return 0
		}))
	case "XPathHREFTitleAll":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			var links, names []string
			if ctx := contextNode(L, 4); ctx != nil {
				links, names = ctx.XPathHREFTitleAll(L.CheckString(1))
			} else {
				links, names = q.XPathHREFTitleAll(L.CheckString(1))
			}
			appendAll(L, 2, links)
			appendAll(L, 3, names)
			return 0
		}))
	case "ParseHTML":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			// Reparsing replaces the document in place, matching x.ParseHTML(s).
			if nq, err := txquery.ParseString(argText(L, 1)); err == nil {
				ud.Value = nq
			}
			return 0
		}))
	default:
		L.Push(lua.LNil)
	}
	return 1
}

// appendAll pushes values into a Strings argument, skipping a missing one so a
// module may pass only the list it cares about.
func appendAll(L *lua.LState, n int, vals []string) {
	ud, ok := L.Get(n).(*lua.LUserData)
	if !ok {
		return
	}
	s, ok := ud.Value.(*Strings)
	if !ok {
		return
	}
	for _, v := range vals {
		s.Add(v)
	}
}

func nodeIndex(L *lua.LState) int {
	n := L.CheckUserData(1).Value.(*txquery.Node)
	switch L.CheckString(2) {
	case "ToString":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(lua.LString(n.Text()))
			return 1
		}))
	case "GetAttribute":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(lua.LString(n.Attribute(L.CheckString(1))))
			return 1
		}))
	case "GetProperty":
		// Upstream returns a value object here, and modules chain straight into
		// .ToString(), so this must be a node rather than a plain string.
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(pushNode(L, n.Property(L.CheckString(1))))
			return 1
		}))
	case "XPathString":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(lua.LString(n.XPathString(L.CheckString(1))))
			return 1
		}))
	case "XPathStringAll":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(lua.LString(n.XPathStringAll(L.CheckString(1), L.OptString(2, txquery.DefaultSeparator))))
			return 1
		}))
	case "XPath":
		L.Push(L.NewFunction(func(L *lua.LState) int {
			L.Push(pushNodeList(L, n.XPath(L.CheckString(1))))
			return 1
		}))
	default:
		L.Push(lua.LNil)
	}
	return 1
}

// stringsArg returns the Strings list at argument n, or nil when it is not one.
func stringsArg(L *lua.LState, n int) *Strings {
	ud, ok := L.Get(n).(*lua.LUserData)
	if !ok {
		return nil
	}
	s, _ := ud.Value.(*Strings)
	return s
}

// contextNode resolves the optional context argument a module passes to
// XPathString/XPath. It accepts either a node or a node list, since modules use
// both: a list comes from an earlier XPath() call, a node from iterating one.
func contextNode(L *lua.LState, n int) *txquery.Node {
	ud, ok := L.Get(n).(*lua.LUserData)
	if !ok {
		return nil
	}
	switch v := ud.Value.(type) {
	case *txquery.Node:
		return v
	case *nodeList:
		if len(v.nodes) > 0 {
			return v.nodes[0]
		}
	}
	return nil
}

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

// registerBuiltins installs the free functions and constants modules expect.
// maxSleep caps what a module can ask to wait for. The longest deliberate
// pause upstream uses is a few seconds.
const maxSleep = 30 * time.Second

func registerBuiltins(L *lua.LState) {
	L.SetGlobal("no_error", lua.LNumber(noError))
	L.SetGlobal("net_problem", lua.LNumber(netProblem))
	L.SetGlobal("information_not_found", lua.LNumber(informationNotFound))
	L.SetGlobal("asUnknown", lua.LNumber(asUnknown))
	L.SetGlobal("asChecking", lua.LNumber(asChecking))
	L.SetGlobal("asValid", lua.LNumber(asValid))
	L.SetGlobal("asInvalid", lua.LNumber(asInvalid))

	// sleep(milliseconds) is an upstream global. Twelve modules use it, and
	// they are not being polite: a site that drip-feeds images over repeated
	// requests gives back only a couple per request without a pause between
	// them, so skipping the wait loses pages.
	//
	// It honours the scrape's context, so cancelling a download does not have
	// to wait out a module's idea of a reasonable delay, and it is capped
	// because a module asking to sleep for an hour has gone wrong.
	L.SetGlobal("sleep", L.NewFunction(func(L *lua.LState) int {
		d := time.Duration(L.CheckInt64(1)) * time.Millisecond
		if d <= 0 {
			return 0
		}
		if d > maxSleep {
			d = maxSleep
		}
		ctx := L.Context()
		if ctx == nil {
			time.Sleep(d)
			return 0
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			L.RaiseError("sleep: %v", ctx.Err())
		}
		return 0
	}))
	L.SetGlobal("CreateTXQuery", L.NewFunction(func(L *lua.LState) int {
		// The argument is usually HTTP.Document, which is userdata, not a string.
		q, err := txquery.ParseString(argText(L, 1))
		if err != nil {
			L.Push(lua.LNil)
			return 1
		}
		L.Push(pushQuery(L, q))
		return 1
	}))
	L.SetGlobal("GetBetween", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(GetBetween(L.CheckString(1), L.CheckString(2), L.CheckString(3))))
		return 1
	}))
	L.SetGlobal("SeparateLeft", L.NewFunction(func(L *lua.LState) int {
		s, sep := L.CheckString(1), L.CheckString(2)
		if left, _, ok := strings.Cut(s, sep); ok {
			L.Push(lua.LString(left))
		} else {
			L.Push(lua.LString(s))
		}
		return 1
	}))
	L.SetGlobal("SeparateRight", L.NewFunction(func(L *lua.LState) int {
		s, sep := L.CheckString(1), L.CheckString(2)
		if _, right, ok := strings.Cut(s, sep); ok {
			L.Push(lua.LString(right))
		} else {
			L.Push(lua.LString(s))
		}
		return 1
	}))
	L.SetGlobal("Trim", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(strings.TrimSpace(L.CheckString(1))))
		return 1
	}))
	L.SetGlobal("MaybeFillHost", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(MaybeFillHost(L.CheckString(1), L.CheckString(2))))
		return 1
	}))
	L.SetGlobal("MangaInfoStatusIfPos", L.NewFunction(func(L *lua.LState) int {
		L.Push(lua.LString(MangaInfoStatusIfPos(
			L.CheckString(1),
			L.OptString(2, defaultOngoing), L.OptString(3, defaultCompleted),
			L.OptString(4, defaultHiatus), L.OptString(5, defaultDropped))))
		return 1
	}))
}
