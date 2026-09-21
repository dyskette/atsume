package txquery

import (
	"encoding/json"
	"strconv"

	"golang.org/x/net/html"
)

// itemTag names the synthetic element wrapping each array member, so that a
// `/*` step iterates the array the way the XQuery `()` and `?*` forms do.
const itemTag = "_"

// BuildJSONTree renders a decoded JSON value as an html.Node tree, letting the
// same XPath engine serve both HTML and JSON documents. Objects become elements
// named for their key, arrays become repeated <_> elements, scalars become text.
func BuildJSONTree(v any) *html.Node {
	root := &html.Node{Type: html.DocumentNode}
	appendValue(root, v)
	return root
}

func appendValue(parent *html.Node, v any) {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			el := &html.Node{Type: html.ElementNode, Data: k}
			parent.AppendChild(el)
			appendValue(el, child)
		}
	case []any:
		for _, child := range t {
			el := &html.Node{Type: html.ElementNode, Data: itemTag}
			parent.AppendChild(el)
			appendValue(el, child)
		}
	case nil:
		// A JSON null is a value, and TXQuery renders it as the text "null".
		// Contributing nothing instead made it indistinguishable from an
		// absent property, and nineteen modules tell the two apart by
		// comparing against that exact string. MangaDex drops every chapter
		// whose externalUrl is not "null", so an empty string there meant a
		// series with chapters listed none, with no error anywhere.
		//
		// A property that is absent still yields no element at all, so the
		// distinction upstream draws is preserved.
		parent.AppendChild(&html.Node{Type: html.TextNode, Data: "null"})
	case string:
		parent.AppendChild(&html.Node{Type: html.TextNode, Data: t})
	case bool:
		parent.AppendChild(&html.Node{Type: html.TextNode, Data: strconv.FormatBool(t)})
	case float64:
		parent.AppendChild(&html.Node{Type: html.TextNode, Data: strconv.FormatFloat(t, 'f', -1, 64)})
	}
}

// ParseJSONTree decodes src and renders it via BuildJSONTree.
func ParseJSONTree(src []byte) (*html.Node, error) {
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		return nil, err
	}
	return BuildJSONTree(v), nil
}
