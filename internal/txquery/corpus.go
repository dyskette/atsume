package txquery

import (
	"io/fs"
	"path"
	"regexp"
	"strings"
)

// Expression is one XPath call site found in a Lua module.
type Expression struct {
	Expr   string // the expression, with Lua concatenation collapsed
	Method string // the TXQuery method it was passed to
	File   string // the module it came from
}

// placeholder stands in for a Lua expression spliced into an XPath string, so
// that the result is still syntactically an XPath.
const placeholder = "PH"

var callRe = regexp.MustCompile(`\.(XPathStringAll|XPathHREFTitleAll|XPathHREFAll|XPathString|XPathCount|XPath)\s*\(`)

// ExtractExpressions walks a FMD2 lua tree and returns every XPath expression
// passed to a TXQuery method.
//
// It exists so the upstream drift check is a plain Go test: re-extract against a
// new upstream revision, re-run Rewrite over the result, and fail when the share
// of expressions that still compile drops.
func ExtractExpressions(fsys fs.FS, roots ...string) ([]Expression, error) {
	var out []Expression
	for _, root := range roots {
		err := fs.WalkDir(fsys, root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || path.Ext(p) != ".lua" {
				return err
			}
			b, err := fs.ReadFile(fsys, p)
			if err != nil {
				return err
			}
			out = append(out, extractFile(string(b), p)...)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func extractFile(src, name string) []Expression {
	var out []Expression
	for _, m := range callRe.FindAllStringSubmatchIndex(src, -1) {
		args := luaArgs(src, m[1]-1)
		if len(args) == 0 {
			continue
		}
		expr, ok := luaConcat(args[0])
		if !ok || strings.TrimSpace(expr) == "" {
			continue // a variable-only argument carries nothing to check
		}
		out = append(out, Expression{Expr: expr, Method: src[m[2]:m[3]], File: name})
	}
	return out
}

// luaString returns the contents of the Lua literal starting at src[i] and the
// index just past it.
func luaString(src string, i int) (string, int) {
	if strings.HasPrefix(src[i:], "[[") {
		if end := strings.Index(src[i+2:], "]]"); end >= 0 {
			return src[i+2 : i+2+end], i + 2 + end + 2
		}
		return src[i+2:], len(src)
	}
	q := src[i]
	var b strings.Builder
	for j := i + 1; j < len(src); j++ {
		switch {
		case src[j] == '\\' && j+1 < len(src):
			switch src[j+1] {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			default:
				b.WriteByte(src[j+1])
			}
			j++
		case src[j] == q:
			return b.String(), j + 1
		default:
			b.WriteByte(src[j])
		}
	}
	return b.String(), len(src)
}

// luaArgs splits the argument list whose '(' sits at src[open].
func luaArgs(src string, open int) []string {
	depth, start := 1, open+1
	var out []string
	for i := open + 1; i < len(src); i++ {
		if c := src[i]; c == '\'' || c == '"' || strings.HasPrefix(src[i:], "[[") {
			_, next := luaString(src, i)
			i = next - 1
			continue
		}
		switch src[i] {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			depth--
			if depth == 0 {
				return append(out, src[start:i])
			}
		case ',':
			if depth == 1 {
				out = append(out, src[start:i])
				start = i + 1
			}
		}
	}
	return out
}

// luaConcat collapses a Lua concatenation into a literal, substituting a
// placeholder for every non-literal term. It reports whether any literal was
// present at all.
func luaConcat(expr string) (string, bool) {
	var b strings.Builder
	sawLiteral := false
	for i := 0; i < len(expr); {
		switch {
		case expr[i] == ' ' || expr[i] == '\t' || expr[i] == '\n' || expr[i] == '\r':
			i++
		case expr[i] == '\'' || expr[i] == '"' || strings.HasPrefix(expr[i:], "[["):
			text, next := luaString(expr, i)
			b.WriteString(text)
			sawLiteral = true
			i = next
		case strings.HasPrefix(expr[i:], ".."):
			i += 2
		default:
			// a non-literal term: skip to the next top-level ".."
			depth := 0
			j := i
			for ; j < len(expr); j++ {
				switch expr[j] {
				case '(', '[', '{':
					depth++
				case ')', ']', '}':
					depth--
				}
				if depth == 0 && strings.HasPrefix(expr[j:], "..") && !strings.HasPrefix(expr[j:], "...") {
					break
				}
			}
			b.WriteString(placeholder)
			i = j
		}
	}
	return b.String(), sawLiteral
}
