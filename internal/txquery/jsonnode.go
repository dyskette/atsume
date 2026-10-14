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
//
// A trailing comma before a closing bracket or brace is accepted, as FMD2
// accepts it: internettools gives json() its default parser, which FMD2's
// engine configures with jpoAllowTrailingComma (data/xquery.pas, where
// DefaultJSONParser.options is set). Sites that embed a JavaScript object
// literal as their page data, such as the MultiChan template's `var data`,
// rely on it; strict decoding rejected the whole object and the chapter came
// back with no pages.
//
// Upstream's parse-json() is stricter and would reject the same input. Both
// share one parse here, so atsume is the more lenient of the two there; that
// can only turn a failure into a result, never change a valid document's.
func ParseJSONTree(src []byte) (*html.Node, error) {
	var v any
	if err := json.Unmarshal(src, &v); err != nil {
		lenient, changed := dropTrailingCommas(src)
		if !changed || json.Unmarshal(lenient, &v) != nil {
			return nil, err
		}
	}
	return BuildJSONTree(v), nil
}

// dropTrailingCommas removes each comma that only whitespace separates from
// the `]` or `}` after it, leaving string contents alone. It reports whether
// it removed any, so a document it cannot help is not decoded twice.
func dropTrailingCommas(src []byte) ([]byte, bool) {
	out := make([]byte, 0, len(src))
	changed := false
	inString, escaped := false, false
	for i := 0; i < len(src); i++ {
		c := src[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			out = append(out, c)
			continue
		}
		if c == '"' {
			inString = true
		} else if c == ',' {
			j := i + 1
			for j < len(src) && isJSONSpace(src[j]) {
				j++
			}
			if j < len(src) && (src[j] == ']' || src[j] == '}') {
				changed = true
				continue
			}
		}
		out = append(out, c)
	}
	return out, changed
}

// isJSONSpace reports whether c is whitespace in JSON's grammar.
func isJSONSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r'
}
