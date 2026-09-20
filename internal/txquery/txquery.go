// Package txquery evaluates the TXQuery expressions used by FMD2's Lua website
// modules.
//
// FMD2 evaluates those expressions with Pascal's internettools engine, which
// speaks XQuery 3.1. This package instead rewrites each expression into XPath
// 1.0 (see rewrite.go) and evaluates it with github.com/antchfx/xpath,
// performing the handful of remaining operations — JSON parsing, sequence
// joining, regex replacement — around the evaluation.
//
// Semantics follow baseunits/XQueryEngineHTML.pas: XPathString does not trim,
// XPathStringAll trims every value, skips empty ones, and defaults to ", ".
package txquery

import (
	"io"
	"regexp"
	"strconv"
	"strings"

	"github.com/antchfx/htmlquery"
	"github.com/antchfx/xpath"
	"golang.org/x/net/html"
)

// DefaultSeparator matches AddSeparatorString's default in FMD2.
const DefaultSeparator = ", "

// Query is a parsed document. The same Query serves both HTML and JSON
// expressions: a document is parsed as HTML eagerly and re-parsed as a JSON
// node tree on first use of a json() or ?-lookup expression, mirroring how
// CreateTXQuery(HTTP.Document) is used for either in the Lua modules.
type Query struct {
	raw      []byte
	htmlTree *html.Node
	jsonTree *html.Node
	jsonDone bool
}

// Node is a single result node, exposed to Lua for iteration.
type Node struct {
	n *html.Node
	q *Query
}

// Parse reads a document. It never fails on malformed markup: html.Parse
// repairs it, matching the repairMissing*Tags settings FMD2 uses.
func Parse(r io.Reader) (*Query, error) {
	raw, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	return ParseBytes(raw)
}

// ParseBytes reads a document already held in memory.
func ParseBytes(raw []byte) (*Query, error) {
	doc, err := html.Parse(strings.NewReader(string(raw)))
	if err != nil {
		return nil, err
	}
	return &Query{raw: raw, htmlTree: doc}, nil
}

// ParseString reads a document from a string.
func ParseString(s string) (*Query, error) { return ParseBytes([]byte(s)) }

// tree picks the document an expression should run against.
func (q *Query) tree(needJSON bool) *html.Node {
	if !needJSON {
		return q.htmlTree
	}
	if !q.jsonDone {
		q.jsonDone = true
		if t, err := ParseJSONTree(q.raw); err == nil {
			q.jsonTree = t
		}
	}
	if q.jsonTree == nil {
		return q.htmlTree // not JSON after all; let the expression come up empty
	}
	return q.jsonTree
}

// eval rewrites and evaluates expr, returning the raw antchfx result.
func (q *Query) eval(expr string, ctx *html.Node) (any, Caps, error) {
	out, caps := Rewrite(expr)
	e, err := xpath.Compile(out)
	if err != nil {
		return nil, caps, err
	}
	root := ctx
	if root == nil {
		root = q.tree(caps.JSON)
	}
	return e.Evaluate(htmlquery.CreateXPathNavigator(root)), caps, nil
}

// values flattens a result into the string sequence FMD2 would iterate.
func values(v any) []string {
	switch t := v.(type) {
	case nil:
		return nil
	case string:
		return []string{t}
	case float64:
		return []string{strconv.FormatFloat(t, 'f', -1, 64)}
	case bool:
		return []string{strconv.FormatBool(t)}
	case *xpath.NodeIterator:
		var out []string
		for t.MoveNext() {
			out = append(out, t.Current().Value())
		}
		return out
	}
	return nil
}

// postProcess applies the operations the rewrite deferred to the host.
func postProcess(vals []string, caps Caps) []string {
	if caps.Replace && caps.ReplaceRe != "" {
		if re, err := regexp.Compile(caps.ReplaceRe); err == nil {
			for i, v := range vals {
				vals[i] = re.ReplaceAllString(v, caps.ReplaceTo)
			}
		}
	}
	return vals
}

// XPathString returns the first result as a string. It does not trim, matching
// TXQueryEngineHTML.EvalString.
func (q *Query) XPathString(expr string) string {
	return q.xpathString(expr, nil)
}

func (q *Query) xpathString(expr string, ctx *html.Node) string {
	v, caps, err := q.eval(expr, ctx)
	if err != nil {
		return ""
	}
	vals := postProcess(values(v), caps)
	// string-join collapses the whole sequence even in the single-value form,
	// which is how Madara builds MANGAINFO.Summary.
	if caps.StringJoin {
		return joinAll(vals, sepOr(caps.JoinSep, DefaultSeparator))
	}
	if len(vals) == 0 {
		return ""
	}
	return vals[0]
}

// XPathStringAll joins every result, trimming each value and skipping the empty
// ones, matching AddSeparatorString.
func (q *Query) XPathStringAll(expr string, sep ...string) string {
	return q.xpathStringAll(expr, nil, sep...)
}

func (q *Query) xpathStringAll(expr string, ctx *html.Node, sep ...string) string {
	v, caps, err := q.eval(expr, ctx)
	if err != nil {
		return ""
	}
	s := DefaultSeparator
	if len(sep) > 0 {
		s = sep[0]
	} else if caps.StringJoin {
		s = sepOr(caps.JoinSep, DefaultSeparator)
	}
	return joinAll(postProcess(values(v), caps), s)
}

func sepOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

func joinAll(vals []string, sep string) string {
	var out []string
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return strings.Join(out, sep)
}

// XPathValues returns each result as a trimmed string, dropping empty ones.
// It backs the XPathStringAll(expr, list) overload that fills a TStringList.
func (q *Query) XPathValues(expr string, ctx *html.Node) []string {
	v, caps, err := q.eval(expr, ctx)
	if err != nil {
		return nil
	}
	var out []string
	for _, s := range postProcess(values(v), caps) {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// XPathCount returns the number of results.
func (q *Query) XPathCount(expr string) int {
	v, _, err := q.eval(expr, nil)
	if err != nil {
		return 0
	}
	if it, ok := v.(*xpath.NodeIterator); ok {
		n := 0
		for it.MoveNext() {
			n++
		}
		return n
	}
	return 0
}

// XPath returns the matching nodes for iteration from Lua.
func (q *Query) XPath(expr string) []*Node {
	out, caps := Rewrite(expr)
	e, err := xpath.Compile(out)
	if err != nil {
		return nil
	}
	root := q.tree(caps.JSON)
	v, ok := e.Evaluate(htmlquery.CreateXPathNavigator(root)).(*xpath.NodeIterator)
	if !ok {
		return nil
	}
	var nodes []*Node
	for v.MoveNext() {
		// NodeNavigator is reused across MoveNext, so resolve to the real node.
		if nav, ok := v.Current().(*htmlquery.NodeNavigator); ok {
			nodes = append(nodes, &Node{n: nav.Current(), q: q})
		}
	}
	return nodes
}

// XPathHREFAll extracts the href and text of every match, the pairing FMD2 uses
// to fill the LINKS and NAMES lists.
func (q *Query) XPathHREFAll(expr string) (links, names []string) {
	for _, n := range q.XPath(expr) {
		links = append(links, n.Attribute("href"))
		names = append(names, strings.TrimSpace(n.Text()))
	}
	return
}

// XPathHREFTitleAll is XPathHREFAll but reads the title attribute for names.
func (q *Query) XPathHREFTitleAll(expr string) (links, names []string) {
	for _, n := range q.XPath(expr) {
		links = append(links, n.Attribute("href"))
		title := n.Attribute("title")
		if title == "" {
			title = strings.TrimSpace(n.Text())
		}
		names = append(names, title)
	}
	return
}

// Text returns the node's string value.
func (n *Node) Text() string { return htmlquery.InnerText(n.n) }

// Attribute returns an attribute value, or "" when absent.
func (n *Node) Attribute(name string) string { return htmlquery.SelectAttr(n.n, name) }

// XPathString evaluates an expression relative to this node, which is how the
// modules read fields out of an iterated JSON member.
func (n *Node) XPathString(expr string) string { return n.q.xpathString(expr, n.n) }

// XPathStringAll evaluates a joining expression relative to this node.
func (n *Node) XPathStringAll(expr string, sep ...string) string {
	return n.q.xpathStringAll(expr, n.n, sep...)
}

// XPath evaluates a node-set expression relative to this node, which templates
// use to walk a volume's chapters after iterating the volume list.
func (n *Node) XPath(expr string) []*Node {
	out, _ := Rewrite(expr)
	e, err := xpath.Compile(out)
	if err != nil {
		return nil
	}
	v, ok := e.Evaluate(htmlquery.CreateXPathNavigator(n.n)).(*xpath.NodeIterator)
	if !ok {
		return nil
	}
	var nodes []*Node
	for v.MoveNext() {
		if nav, ok := v.Current().(*htmlquery.NodeNavigator); ok {
			nodes = append(nodes, &Node{n: nav.Current(), q: n.q})
		}
	}
	return nodes
}
