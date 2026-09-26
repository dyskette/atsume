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
	// text overrides the node's string value. It carries an attribute's value,
	// because antchfx reports an attribute match as its owning element, and it
	// lets an absent JSON property be returned as an empty node rather than nil.
	text    string
	hasText bool
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
		q.jsonTree = q.buildJSONTree()
	}
	if q.jsonTree == nil {
		return q.htmlTree // not JSON after all; let the expression come up empty
	}
	return q.jsonTree
}

// buildJSONTree parses the document as JSON.
//
// The raw body is tried first, which covers an ordinary JSON response. When
// that fails the document's text content is tried instead: modules run the body
// through HTMLEncode before handing it to CreateTXQuery, so that a `<` inside a
// JSON string is not parsed as markup, and the entities have to be decoded
// again before the result is JSON. internettools reads the string value of the
// document for json(), which decodes them as a side effect; doing the same
// keeps both shapes working.
func (q *Query) buildJSONTree() *html.Node {
	if t, err := ParseJSONTree(q.raw); err == nil {
		return t
	}
	text := htmlquery.InnerText(q.htmlTree)
	if text == "" {
		return nil
	}
	if t, err := ParseJSONTree([]byte(text)); err == nil {
		return t
	}
	return nil
}

// eval rewrites and evaluates expr, returning the raw antchfx result.
func (q *Query) eval(expr string, ctx *html.Node) (any, Caps, error) {
	out, caps := Rewrite(expr)
	e, err := xpath.Compile(out)
	if err != nil {
		return nil, caps, err
	}
	return e.Evaluate(htmlquery.CreateXPathNavigator(q.rootFor(caps, ctx))), caps, nil
}

// rootFor picks the node an expression evaluates against.
func (q *Query) rootFor(caps Caps, ctx *html.Node) *html.Node {
	if ctx != nil {
		return ctx
	}
	if caps.JSONSource != "" {
		// The JSON is not the response body but the result of an expression
		// over it, so it is extracted and parsed per call rather than cached.
		if t := q.jsonFrom(caps.JSONSource); t != nil {
			return t
		}
		return q.htmlTree
	}
	return q.tree(caps.JSON)
}

// jsonFrom evaluates src against the document and parses the result as JSON.
func (q *Query) jsonFrom(src string) *html.Node {
	text := q.XPathString(src)
	if strings.TrimSpace(text) == "" {
		return nil
	}
	t, err := ParseJSONTree([]byte(text))
	if err != nil {
		return nil
	}
	return t
}

// evalValues returns the string sequence an expression produces.
//
// A sequence constructor whose members are not all node-sets — `(//a, concat(…))`
// is the common shape — cannot be folded into a union, because XPath 1.0 unions
// take node-sets only and the string member is silently dropped. Those are
// evaluated member by member and concatenated instead.
func (q *Query) evalValues(expr string, ctx *html.Node) ([]string, Caps) {
	out, caps := Rewrite(expr)
	if len(caps.Parts) == 0 {
		v, _, err := q.eval(expr, ctx)
		if err != nil {
			return nil, caps
		}
		return postProcess(values(v), caps), caps
	}

	var all []string
	for _, part := range caps.Parts {
		e, err := xpath.Compile(part)
		if err != nil {
			continue
		}
		all = append(all, values(e.Evaluate(htmlquery.CreateXPathNavigator(q.rootFor(caps, ctx))))...)
	}
	_ = out
	return postProcess(all, caps), caps
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
	vals, caps := q.evalValues(expr, ctx)
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
	vals, caps := q.evalValues(expr, ctx)
	s := DefaultSeparator
	if len(sep) > 0 {
		s = sep[0]
	} else if caps.StringJoin {
		s = sepOr(caps.JoinSep, DefaultSeparator)
	}
	return joinAll(vals, s)
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

// Values evaluates expr the way modules' XPath calls do, returning each
// result as a trimmed string and the error the module calls swallow: a
// selector being tried out should say why it matched nothing.
func (q *Query) Values(expr string) ([]string, error) {
	var err error
	if _, caps := Rewrite(expr); len(caps.Parts) == 0 {
		_, _, err = q.eval(expr, nil)
	} else {
		for _, part := range caps.Parts {
			if _, cerr := xpath.Compile(part); cerr != nil && err == nil {
				err = cerr
			}
		}
	}
	vals, _ := q.evalValues(expr, nil)
	var out []string
	for _, s := range vals {
		if t := strings.TrimSpace(s); t != "" {
			out = append(out, t)
		}
	}
	return out, err
}

// XPathValues returns each result as a trimmed string, dropping empty ones.
// It backs the XPathStringAll(expr, list) overload that fills a TStringList.
func (q *Query) XPathValues(expr string, ctx *html.Node) []string {
	vals, _ := q.evalValues(expr, ctx)
	var out []string
	for _, s := range vals {
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
	v, ok := e.Evaluate(htmlquery.CreateXPathNavigator(q.rootFor(caps, nil))).(*xpath.NodeIterator)
	if !ok {
		return nil
	}
	return collectNodes(v, q)
}

// collectNodes materialises an iterator, preserving attribute values.
func collectNodes(it *xpath.NodeIterator, q *Query) []*Node {
	var nodes []*Node
	for it.MoveNext() {
		// The navigator is reused across MoveNext, so resolve to a real node.
		nav, ok := it.Current().(*htmlquery.NodeNavigator)
		if !ok {
			continue
		}
		node := &Node{n: nav.Current(), q: q}
		if nav.NodeType() == xpath.AttributeNode {
			node.hasText, node.text = true, nav.Value()
		}
		nodes = append(nodes, node)
	}
	return nodes
}

// XPathHREFAll extracts the href and text of every match, the pairing FMD2 uses
// to fill the LINKS and NAMES lists.
func (q *Query) XPathHREFAll(expr string) (links, names []string) {
	return hrefAll(q.XPath(expr), false)
}

// XPathHREFTitleAll is XPathHREFAll but reads the title attribute for names.
func (q *Query) XPathHREFTitleAll(expr string) (links, names []string) {
	return hrefAll(q.XPath(expr), true)
}

// XPathHREFAll extracts hrefs relative to this node.
func (n *Node) XPathHREFAll(expr string) (links, names []string) {
	return hrefAll(n.XPath(expr), false)
}

// XPathHREFTitleAll extracts hrefs and titles relative to this node.
func (n *Node) XPathHREFTitleAll(expr string) (links, names []string) {
	return hrefAll(n.XPath(expr), true)
}

// XPathCount counts matches relative to this node.
func (n *Node) XPathCount(expr string) int { return len(n.XPath(expr)) }

// Property returns a named child, which on the JSON node tree is how a property
// is reached.
//
// A missing property yields an empty node rather than nil, because modules
// chain straight into .ToString() on the result and upstream returns a value
// object there, not nothing.
func (n *Node) Property(name string) *Node {
	if nodes := n.XPath(name); len(nodes) > 0 {
		return nodes[0]
	}
	return &Node{q: n.q, hasText: true}
}

// Attribute returns an attribute value, or "" when absent.
func (n *Node) Attribute(name string) string {
	if n.n == nil {
		return ""
	}
	return htmlquery.SelectAttr(n.n, name)
}

func hrefAll(nodes []*Node, useTitle bool) (links, names []string) {
	for _, n := range nodes {
		links = append(links, n.Attribute("href"))
		name := strings.TrimSpace(n.Text())
		if useTitle {
			if title := n.Attribute("title"); title != "" {
				name = title
			}
		}
		// An anchor with no text of its own — one wrapping only a cover
		// image, say — falls back to its title, which is where the name
		// then is. Upstream takes the empty string, so those sites list
		// rows with nothing written on them; FanFox is one, and its module
		// has no way to know the markup moved the title into an attribute.
		//
		// This can only replace nothing with something, so no site that
		// works today changes.
		if name == "" {
			name = strings.TrimSpace(n.Attribute("title"))
		}
		names = append(names, name)
	}
	return
}

// Text returns the node's string value.
func (n *Node) Text() string {
	if n.hasText {
		return n.text
	}
	if n.n == nil {
		return ""
	}
	return htmlquery.InnerText(n.n)
}

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
	return collectNodes(v, n.q)
}
