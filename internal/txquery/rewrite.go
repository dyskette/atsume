package txquery

import (
	"fmt"
	"strings"
)

// Caps records which host-side capabilities an expression needs once the
// XQuery-only constructs have been rewritten away. The rewritten expression is
// plain XPath 1.0; the flags tell the host what to do around it (parse the
// document as JSON, join the result sequence, apply a regex, and so on).
type Caps struct {
	JSON       bool   // json(*) / parse-json(.) — evaluate against a JSON node tree
	StringJoin bool   // string-join(seq, sep) — host joins the node-set
	JoinSep    string // the separator from string-join, when StringJoin is set
	ReplaceRe  string // the pattern from replace(), when Replace is set
	ReplaceTo  string // the replacement from replace(), when Replace is set
	Case       bool   // upper-case()/lower-case() — rewritten to translate()
	Replace    bool   // replace(s, re, with) — host applies a regex
	CSS        bool   // css("sel") — selector converted to a path
	SimpleMap  bool   // the "!" simple map operator
	ConcatOp   bool   // the "||" string concatenation operator
	ParenStep  bool   // parenthesised path step, e.g. /(a|b)[last()]
	Tokenize   bool   // tokenize() — host splits
	FuncStep   bool   // a function call in step position, e.g. //p/substring-after(.,":")
	Lookup     bool   // the XQuery 3.1 "?" lookup operator over JSON
	Sequence   bool   // (a, b, c) sequence constructor — folded to a union
	JSONFns    bool   // jn:members() / jn:keys()
}

func (c Caps) names() []string {
	var out []string
	for _, p := range []struct {
		on bool
		n  string
	}{
		{c.JSON, "json"}, {c.StringJoin, "string-join"}, {c.Case, "case"},
		{c.Replace, "replace"}, {c.CSS, "css"}, {c.SimpleMap, "simple-map"},
		{c.ConcatOp, "concat-op"}, {c.ParenStep, "paren-step"}, {c.Tokenize, "tokenize"},
		{c.Lookup, "lookup-op"}, {c.Sequence, "sequence"}, {c.JSONFns, "jn:*"},
		{c.FuncStep, "func-step"},
	} {
		if p.on {
			out = append(out, p.n)
		}
	}
	return out
}

// skipLiteral reports the index just past the string literal starting at s[i],
// or -1 if s[i] does not begin one. XPath literals have no escape sequences.
func skipLiteral(s string, i int) int {
	if i >= len(s) || (s[i] != '\'' && s[i] != '"') {
		return -1
	}
	q := s[i]
	for j := i + 1; j < len(s); j++ {
		if s[j] != q {
			continue
		}
		if j+1 < len(s) && s[j+1] == q { // "" is an escaped quote, not the end
			j++
			continue
		}
		return j + 1
	}
	return len(s)
}

// matchParen returns the index of the ')' closing the '(' at s[open].
func matchParen(s string, open int) int {
	depth := 0
	for i := open; i < len(s); i++ {
		if j := skipLiteral(s, i); j > 0 {
			i = j - 1
			continue
		}
		switch s[i] {
		case '(':
			depth++
		case ')':
			if depth--; depth == 0 {
				return i
			}
		}
	}
	return -1
}

// findCall locates a call to fn at the top level of s, ignoring matches that are
// part of a longer QName (so "json" does not match inside "parse-json").
func findCall(s, fn string, from int) (start, open int) {
	for i := from; i+len(fn) < len(s); i++ {
		if j := skipLiteral(s, i); j > 0 {
			i = j - 1
			continue
		}
		if !strings.HasPrefix(s[i:], fn) {
			continue
		}
		if i > 0 {
			p := s[i-1]
			if p == '-' || p == ':' || p == '_' || isNameChar(p) {
				continue
			}
		}
		k := i + len(fn)
		for k < len(s) && s[k] == ' ' {
			k++
		}
		if k < len(s) && s[k] == '(' {
			return i, k
		}
	}
	return -1, -1
}

func isNameChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9'
}

// splitArgs splits the text between parentheses on top-level commas.
func splitArgs(s string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		if j := skipLiteral(s, i); j > 0 {
			i = j - 1
			continue
		}
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		case ',':
			if depth == 0 {
				out = append(out, strings.TrimSpace(s[start:i]))
				start = i + 1
			}
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// rewriteJSON turns `json(*)` and its XQuery property chain into a location
// path over the synthetic node tree the host builds from the JSON body:
//
//	json(*)                      -> /
//	json(*).data.chapters()      -> /data/chapters/*
//	json(*).sources()[1].images() -> /sources/*[1]/images/*
func rewriteJSON(e string, c *Caps) string {
	for _, fn := range []string{"parse-json", "json"} {
		for {
			start, open := findCall(e, fn, 0)
			if start < 0 {
				break
			}
			close := matchParen(e, open)
			if close < 0 {
				return e
			}
			c.JSON = true
			path, next := consumeChain(e, close+1)
			e = e[:start] + path + e[next:]
		}
	}
	return e
}

// consumeChain reads the `.name`, `()` and `[pred]` suffixes that follow a
// json() call and renders them as a location path.
func consumeChain(s string, i int) (string, int) {
	var b strings.Builder
	wrote := false
	for i < len(s) {
		switch {
		case s[i] == '.' && i+1 < len(s) && (isNameChar(s[i+1]) || s[i+1] == '_'):
			j := i + 1
			for j < len(s) && (isNameChar(s[j]) || s[j] == '_' || s[j] == '-') {
				j++
			}
			b.WriteByte('/')
			b.WriteString(s[i+1 : j])
			wrote = true
			i = j
		case s[i] == '(' && i+1 < len(s) && s[i+1] == ')':
			b.WriteString("/*")
			wrote = true
			i += 2
		case s[i] == '[':
			depth, j := 0, i
			for ; j < len(s); j++ {
				if k := skipLiteral(s, j); k > 0 {
					j = k - 1
					continue
				}
				if s[j] == '[' {
					depth++
				} else if s[j] == ']' {
					if depth--; depth == 0 {
						j++
						break
					}
				}
			}
			if !wrote {
				b.WriteString("/*")
				wrote = true
			}
			b.WriteString(s[i:j])
			i = j
		default:
			goto done
		}
	}
done:
	if !wrote {
		return "/", i
	}
	return b.String(), i
}

// unwrapCall replaces `fn(a, b, ...)` with its first argument, handing the
// remaining arguments to keep, which stores them on the Caps for the host to
// apply after the XPath evaluation.
func unwrapCall(e, fn string, c *Caps, flag *bool, keep func(c *Caps, rest []string)) string {
	for {
		start, open := findCall(e, fn, 0)
		if start < 0 {
			return e
		}
		close := matchParen(e, open)
		if close < 0 {
			return e
		}
		args := splitArgs(e[open+1 : close])
		*flag = true
		if keep != nil && len(args) > 1 {
			keep(c, args[1:])
		}
		e = e[:start] + "(" + args[0] + ")" + e[close+1:]
	}
}

// unquote strips the quotes from an XPath string literal and undoes the
// doubled-quote escape.
func unquote(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 2 && (s[0] == '\'' || s[0] == '"') && s[len(s)-1] == s[0] {
		q := string(s[0])
		return strings.ReplaceAll(s[1:len(s)-1], q+q, q)
	}
	return s
}

const (
	upper = "ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	lower = "abcdefghijklmnopqrstuvwxyz"
)

// rewriteCase expresses upper-case()/lower-case() with XPath 1.0's translate().
func rewriteCase(e string, c *Caps) string {
	for _, p := range []struct{ fn, from, to string }{
		{"lower-case", upper, lower},
		{"upper-case", lower, upper},
	} {
		for {
			start, open := findCall(e, p.fn, 0)
			if start < 0 {
				break
			}
			close := matchParen(e, open)
			if close < 0 {
				break
			}
			c.Case = true
			arg := splitArgs(e[open+1 : close])[0]
			e = e[:start] + fmt.Sprintf("translate(%s,'%s','%s')", arg, p.from, p.to) + e[close+1:]
		}
	}
	return e
}

// rewriteCSS converts the simple descendant/child selectors the corpus uses
// into equivalent location paths.
func rewriteCSS(e string, c *Caps) string {
	for {
		start, open := findCall(e, "css", 0)
		if start < 0 {
			return e
		}
		close := matchParen(e, open)
		if close < 0 {
			return e
		}
		sel := strings.Trim(splitArgs(e[open+1 : close])[0], `'"`)
		c.CSS = true
		e = e[:start] + cssToXPath(sel) + e[close+1:]
	}
}

func cssToXPath(sel string) string {
	var b strings.Builder
	child := false
	for _, tok := range strings.Fields(strings.ReplaceAll(sel, ">", " > ")) {
		if tok == ">" {
			child = true
			continue
		}
		if b.Len() == 0 {
			b.WriteString("//")
		} else if child {
			b.WriteString("/")
		} else {
			b.WriteString("//")
		}
		child = false
		name, preds := "*", ""
		rest := tok
		if i := strings.IndexAny(rest, ".#"); i != 0 {
			if i < 0 {
				name, rest = rest, ""
			} else {
				name, rest = rest[:i], rest[i:]
			}
		}
		for len(rest) > 0 {
			sep := rest[0]
			rest = rest[1:]
			j := strings.IndexAny(rest, ".#")
			val := rest
			if j >= 0 {
				val, rest = rest[:j], rest[j:]
			} else {
				rest = ""
			}
			if sep == '#' {
				preds += fmt.Sprintf("[@id='%s']", val)
			} else {
				preds += fmt.Sprintf("[contains(concat(' ',@class,' '),' %s ')]", val)
			}
		}
		b.WriteString(name + preds)
	}
	return b.String()
}

// rewriteConcatOp turns the XQuery `||` operator into concat().
func rewriteConcatOp(e string, c *Caps) string {
	if !strings.Contains(e, "||") {
		return e
	}
	parts := splitTopLevel(e, "||")
	if len(parts) < 2 {
		return e
	}
	c.ConcatOp = true
	return "concat(" + strings.Join(parts, ",") + ")"
}

func splitTopLevel(s, op string) []string {
	var out []string
	depth, start := 0, 0
	for i := 0; i < len(s); i++ {
		if j := skipLiteral(s, i); j > 0 {
			i = j - 1
			continue
		}
		switch s[i] {
		case '(', '[':
			depth++
		case ')', ']':
			depth--
		}
		if depth == 0 && strings.HasPrefix(s[i:], op) {
			out = append(out, strings.TrimSpace(s[start:i]))
			i += len(op) - 1
			start = i + 1
		}
	}
	return append(out, strings.TrimSpace(s[start:]))
}

// rewriteLookup turns the XQuery 3.1 lookup operator into path steps over the
// synthetic JSON tree: `authors?*?name` -> `authors/*/name`, `x?2` -> `x/*[2]`.
func rewriteLookup(e string, c *Caps) string {
	var b strings.Builder
	for i := 0; i < len(e); i++ {
		if j := skipLiteral(e, i); j > 0 {
			b.WriteString(e[i:j])
			i = j - 1
			continue
		}
		if e[i] != '?' {
			b.WriteByte(e[i])
			continue
		}
		j := i + 1
		switch {
		case j < len(e) && e[j] == '*':
			b.WriteString("/*")
			i = j
		case j < len(e) && e[j] >= '0' && e[j] <= '9':
			k := j
			for k < len(e) && e[k] >= '0' && e[k] <= '9' {
				k++
			}
			b.WriteString("/*[" + e[j:k] + "]")
			i = k - 1
		case j < len(e) && (isNameChar(e[j]) || e[j] == '_'):
			k := j
			for k < len(e) && (isNameChar(e[k]) || e[k] == '_' || e[k] == '-') {
				k++
			}
			b.WriteString("/" + e[j:k])
			i = k - 1
		default:
			b.WriteByte('?')
			continue
		}
		c.Lookup = true
		c.JSON = true
	}
	return b.String()
}

// rewriteJSONFns maps the internettools JSON helpers onto the node tree.
func rewriteJSONFns(e string, c *Caps) string {
	for _, fn := range []string{"jn:members", "jn:keys", "members", "keys"} {
		for {
			start, open := findCall(e, fn, 0)
			if start < 0 {
				break
			}
			close := matchParen(e, open)
			if close < 0 {
				break
			}
			c.JSONFns, c.JSON = true, true
			arg := splitArgs(e[open+1 : close])[0]
			// keys() needs the host to read property names; members() is /*.
			e = e[:start] + "(" + arg + ")/*" + e[close+1:]
		}
	}
	return e
}

// rewriteSequence folds a sequence constructor into a union, which is what the
// host does with the result anyway (XPathStringAll joins the node-set).
func rewriteSequence(e string, c *Caps) string {
	for i := 0; i < len(e); i++ {
		if j := skipLiteral(e, i); j > 0 {
			i = j - 1
			continue
		}
		if e[i] != '(' {
			continue
		}
		// a grouping paren, not a function call
		k := i - 1
		for k >= 0 && e[k] == ' ' {
			k--
		}
		if k >= 0 && (isNameChar(e[k]) || e[k] == '_' || e[k] == ')' || e[k] == ']') {
			continue
		}
		close := matchParen(e, i)
		if close < 0 {
			continue
		}
		parts := splitArgs(e[i+1 : close])
		if len(parts) < 2 {
			continue
		}
		c.Sequence = true
		repl := "(" + strings.Join(parts, " | ") + ")"
		e = e[:i] + repl + e[close+1:]
		i += len(repl) - 1
	}
	return e
}

// rewriteDottedNames splits XQuery property navigation (`manga.title`) into
// location steps. Only applied to expressions already known to run against a
// JSON node tree, where a dot is never part of a name.
func rewriteDottedNames(e string) string {
	var b strings.Builder
	for i := 0; i < len(e); i++ {
		if j := skipLiteral(e, i); j > 0 {
			b.WriteString(e[i:j])
			i = j - 1
			continue
		}
		// only rewrite a dot sitting between two name characters
		if e[i] == '.' && i > 0 && i+1 < len(e) &&
			(isNameChar(e[i-1]) || e[i-1] == '_') &&
			(isNameChar(e[i+1]) || e[i+1] == '_') &&
			!(e[i-1] >= '0' && e[i-1] <= '9' && e[i+1] >= '0' && e[i+1] <= '9') {
			b.WriteByte('/')
			continue
		}
		b.WriteByte(e[i])
	}
	return b.String()
}

// xpath1Funcs are the functions that may legally appear in step position in a
// TXQuery expression and that XPath 1.0 can evaluate once hoisted.
var xpath1Funcs = map[string]bool{
	"substring-after": true, "substring-before": true, "normalize-space": true,
	"concat": true, "translate": true, "string": true, "number": true,
	"substring": true, "string-length": true, "lower-case": true, "upper-case": true,
}

// rewriteJoinStep flattens `PATH/string-join(REL, sep)` into `PATH/REL`; the
// host performs the join, so only the path has to survive into XPath.
func rewriteJoinStep(e string, c *Caps) string {
	for pass := 0; pass < 8; pass++ {
		slash := -1
		var open int
		depth := 0
		for i := 0; i < len(e); i++ {
			if j := skipLiteral(e, i); j > 0 {
				i = j - 1
				continue
			}
			switch e[i] {
			case '(', '[':
				depth++
				continue
			case ')', ']':
				depth--
				continue
			}
			if depth == 0 && e[i] == '/' && strings.HasPrefix(e[i+1:], "string-join(") {
				slash, open = i, i+1+len("string-join")
				break
			}
		}
		if slash < 0 {
			return e
		}
		close := matchParen(e, open)
		if close < 0 {
			return e
		}
		joinArgs := splitArgs(e[open+1 : close])
		rel := strings.TrimSpace(joinArgs[0])
		rel = strings.TrimPrefix(rel, "./")
		c.StringJoin = true
		if len(joinArgs) > 1 {
			c.JoinSep = unquote(joinArgs[1])
		}
		e = e[:slash] + "/" + rel + e[close+1:]
	}
	return e
}

// rewriteFuncStep hoists a function call out of step position and feeds it the
// preceding path. XPath 1.0 has no `path/f(.)` form: antchfx compiles it but
// evaluates to nothing, so leaving it alone would silently lose data.
//
//	//p[contains(.,"Author")]/substring-after(., ":")
//	-> substring-after(//p[contains(.,"Author")], ":")
func rewriteFuncStep(e string, c *Caps) string {
	for pass := 0; pass < 8; pass++ {
		slash, name, open := findFuncStep(e)
		if slash < 0 {
			return e
		}
		close := matchParen(e, open)
		if close < 0 {
			return e
		}
		path := strings.TrimSpace(e[:slash])
		if path == "" {
			return e
		}
		args := splitArgs(e[open+1 : close])
		for i, a := range args {
			args[i] = substituteContext(a, path)
		}
		c.FuncStep = true
		e = name + "(" + strings.Join(args, ", ") + ")" + e[close+1:]
	}
	return e
}

// findFuncStep locates the first top-level `/name(` whose name is a function.
func findFuncStep(e string) (slash int, name string, open int) {
	depth := 0
	for i := 0; i < len(e); i++ {
		if j := skipLiteral(e, i); j > 0 {
			i = j - 1
			continue
		}
		switch e[i] {
		case '(', '[':
			depth++
			continue
		case ')', ']':
			depth--
			continue
		}
		if depth != 0 || e[i] != '/' {
			continue
		}
		j := i + 1
		if j < len(e) && e[j] == '/' {
			continue // descendant axis
		}
		k := j
		for k < len(e) && (isNameChar(e[k]) || e[k] == '-' || e[k] == '_') {
			k++
		}
		if k == j || k >= len(e) || e[k] != '(' {
			continue
		}
		if !xpath1Funcs[e[j:k]] {
			continue
		}
		return i, e[j:k], k
	}
	return -1, "", -1
}

// substituteContext replaces the context item `.` with the hoisted path.
func substituteContext(arg, path string) string {
	var b strings.Builder
	for i := 0; i < len(arg); i++ {
		if j := skipLiteral(arg, i); j > 0 {
			b.WriteString(arg[i:j])
			i = j - 1
			continue
		}
		if arg[i] != '.' {
			b.WriteByte(arg[i])
			continue
		}
		prev := byte(0)
		if i > 0 {
			prev = arg[i-1]
		}
		next := byte(0)
		if i+1 < len(arg) {
			next = arg[i+1]
		}
		lone := !isNameChar(prev) && prev != '.' && prev != '_' &&
			!isNameChar(next) && next != '.' && next != '_'
		if lone {
			b.WriteString(path)
			continue
		}
		b.WriteByte('.')
	}
	return b.String()
}

// normalizeQuotes rewrites XQuery's doubled-quote escape ("" inside a
// double-quoted literal) into a single-quoted literal, which XPath 1.0 parsers
// accept. Literals containing both quote styles are left alone.
func normalizeQuotes(e string) string {
	var b strings.Builder
	for i := 0; i < len(e); i++ {
		if e[i] != '"' {
			b.WriteByte(e[i])
			continue
		}
		end := skipLiteral(e, i)
		raw := e[i+1 : end-1]
		if !strings.Contains(raw, `""`) {
			b.WriteString(e[i:end])
			i = end - 1
			continue
		}
		text := strings.ReplaceAll(raw, `""`, `"`)
		if strings.Contains(text, "'") {
			b.WriteString(e[i:end]) // both quote styles: leave for the host
		} else {
			b.WriteString("'" + text + "'")
		}
		i = end - 1
	}
	return b.String()
}

// Rewrite applies every pass, returning an XPath 1.0 expression plus the host
// capabilities it depends on.
func Rewrite(e string) (string, Caps) {
	var c Caps
	e = normalizeQuotes(e)
	e = rewriteJSON(e, &c)
	e = rewriteJSONFns(e, &c)
	e = rewriteLookup(e, &c)
	e = rewriteCSS(e, &c)
	e = rewriteCase(e, &c)
	e = rewriteFuncStep(e, &c)
	e = rewriteJoinStep(e, &c)
	e = unwrapCall(e, "string-join", &c, &c.StringJoin, func(c *Caps, rest []string) {
		c.JoinSep = unquote(rest[0])
	})
	e = unwrapCall(e, "replace", &c, &c.Replace, func(c *Caps, rest []string) {
		c.ReplaceRe = unquote(rest[0])
		if len(rest) > 1 {
			c.ReplaceTo = unquote(rest[1])
		}
	})
	e = unwrapCall(e, "tokenize", &c, &c.Tokenize, nil)
	e = rewriteConcatOp(e, &c)
	e = rewriteSequence(e, &c)
	if c.JSON {
		e = rewriteDottedNames(e)
	}
	return strings.TrimSpace(e), c
}
