package scraper

import (
	"io"
	"path/filepath"
	"testing"

	rt "github.com/arnodel/golua/runtime"
)

const queryPage = `<html><head><title> The  Title </title>
<meta property="og:image" content="/cover.jpg"></head><body>
<div class="info"><span class="status">Status: Completed</span><p class="alt">Alt one</p><p class="alt">Alt two</p></div>
<ul class="chapters">
  <li><a href="/c/2" title="Second">Chapter 2</a></li>
  <li><a href="/c/1" title="First">Chapter 1</a></li>
  <li><a href="https://cdn.example/c/0">Chapter 0</a></li>
</ul>
<div id="pages" data-count="3"><img src="/p/1.jpg"><img src="/p/2.jpg"><img data-src="/p/3.jpg"></div>
</body></html>`

const queryJSON = `{"data":{"title":"From JSON","chapters":[{"id":10,"name":"One"},{"id":11,"name":"Two"}]}}`

// runGoluaQuery runs src with the golua bindings and builtins.
func runGoluaQuery(t *testing.T, src string) (string, bool) {
	t.Helper()
	r := newLuaRuntime(io.Discard, filepath.Join(t.TempDir(), "lua"))
	registerGoluaBuiltins(r, t.Context())
	env := r.GlobalEnv()
	env.Set(rt.StringValue("DOC"), pushGoluaDocument(r, &Document{data: []byte(queryPage)}))
	env.Set(rt.StringValue("JSON"), pushGoluaDocument(r, &Document{data: []byte(queryJSON)}))
	env.Set(rt.StringValue("LINKS"), pushGoluaStrings(r, NewStrings()))
	env.Set(rt.StringValue("NAMES"), pushGoluaStrings(r, NewStrings()))
	chunk, err := r.CompileAndLoadLuaChunk("test", []byte(src), rt.TableValue(env))
	if err != nil {
		t.Fatalf("compile %q: %v", src, err)
	}
	v, err := rt.Call1(r.MainThread(), rt.FunctionValue(chunk))
	if err != nil {
		return "", false
	}
	s, _ := v.ToString()
	return s, true
}

// TestGoluaQueryMatchesGopher runs each snippet through both runtimes and
// requires the same result, and the same success or failure.
const querySnippetsPrefix = `local x = CreateTXQuery(DOC) `

var querySnippets = map[string]string{
	// Document
	"document to string": `return DOC.ToString():sub(1, 6)`,
	"document size":      `return tostring(DOC.Size) .. ' ' .. tostring(#DOC)`,
	"document tostring":  `return tostring(DOC):sub(1, 6)`,
	"document unknown":   `return tostring(DOC.Nope)`,

	// CreateTXQuery and the query object
	"from a string":             `return CreateTXQuery('<p>hi</p>').XPathString('//p')`,
	"from a number":             `return CreateTXQuery(5).XPathString('.')`,
	"xpath string":              querySnippetsPrefix + `return x.XPathString('//title')`,
	"xpath attribute":           querySnippetsPrefix + `return x.XPathString('//meta[@property="og:image"]/@content')`,
	"xpath string missing":      querySnippetsPrefix + `return '[' .. x.XPathString('//nothing') .. ']'`,
	"xpath string all":          querySnippetsPrefix + `return x.XPathStringAll('//p[@class="alt"]')`,
	"xpath string all, sep":     querySnippetsPrefix + `return x.XPathStringAll('//p[@class="alt"]', ' | ')`,
	"xpath string all, list":    querySnippetsPrefix + `x.XPathStringAll('//img/@src', LINKS); return LINKS.Count .. LINKS.Text`,
	"xpath string all, context": querySnippetsPrefix + `return x.XPathStringAll('p', ', ', x.XPath('//div[@class="info"]'))`,
	"xpath count":               querySnippetsPrefix + `return tostring(x.XPathCount('//li'))`,
	"xpath count in a URL":      querySnippetsPrefix + `return 'n=' .. x.XPathCount('//li')`,
	"xpath count arithmetic":    querySnippetsPrefix + `return tostring(x.XPathCount('//li') // 2)`,
	"xpath count, context":      querySnippetsPrefix + `return tostring(x.XPathCount('a', x.XPath('//li')))`,
	"xpath string, node ctx":    querySnippetsPrefix + `local n = x.XPath('//li').Get(2); return x.XPathString('a/@href', n)`,
	"xpath string, list ctx":    querySnippetsPrefix + `return x.XPathString('a', x.XPath('//li'))`,
	"href all":                  querySnippetsPrefix + `x.XPathHREFAll('//li/a', LINKS, NAMES); return LINKS.CommaText .. '|' .. NAMES.CommaText`,
	"href all, one list":        querySnippetsPrefix + `x.XPathHREFAll('//li/a', LINKS); return LINKS.CommaText`,
	"href all, context":         querySnippetsPrefix + `x.XPathHREFAll('a', LINKS, NAMES, x.XPath('//li').Get(3)); return LINKS.CommaText .. NAMES.CommaText`,
	"href title all":            querySnippetsPrefix + `x.XPathHREFTitleAll('//li/a', LINKS, NAMES); return NAMES.CommaText`,
	"parse html":                querySnippetsPrefix + `x.ParseHTML('<b>new</b>'); return x.XPathString('//b')`,
	"parse html from document":  querySnippetsPrefix + `x.ParseHTML(DOC); return x.XPathString('//title')`,
	"json":                      `local j = CreateTXQuery(JSON); return j.XPathString('json(*).data.title') .. j.XPathCount('json(*).data.chapters()')`,
	"json string all":           `return CreateTXQuery(JSON).XPathStringAll('json(*).data.chapters().name')`,
	"query unknown":             querySnippetsPrefix + `return tostring(x.Nope)`,
	"xpath without expression":  querySnippetsPrefix + `return x.XPathString()`,

	// Node lists
	"list count and length": querySnippetsPrefix + `local l = x.XPath('//li'); return l.Count .. #l`,
	"list iterate":          querySnippetsPrefix + `local s = '' for n in x.XPath('//li/a').Get() do s = s .. n.ToString() .. ';' end return s`,
	"list iterate, chained": querySnippetsPrefix + `local s = 0 for _ in CreateTXQuery(DOC).XPath('//img').Get() do s = s + 1 end return tostring(s)`,
	"list get by index":     querySnippetsPrefix + `return x.XPath('//li/a').Get(1).ToString()`,
	"list get out of range": querySnippetsPrefix + `return tostring(x.XPath('//li').Get(9))`,
	"list index":            querySnippetsPrefix + `return x.XPath('//li/a')[2].GetAttribute('href')`,
	"list index float":      querySnippetsPrefix + `return x.XPath('//li/a')[2.5].GetAttribute('href')`,
	"list index zero":       querySnippetsPrefix + `return tostring(x.XPath('//li')[0])`,
	"list empty iterate":    querySnippetsPrefix + `local n = 0 for _ in x.XPath('//none').Get() do n = n + 1 end return n .. x.XPath('//none').Count`,
	"list loop by count":    querySnippetsPrefix + `local l = x.XPath('//li/a'); local s = '' for i = 1, l.Count do s = s .. l.Get(i).GetAttribute('title') end return s`,

	// Nodes
	"node to string":             querySnippetsPrefix + `return x.XPath('//span').Get(1).ToString()`,
	"node attribute":             querySnippetsPrefix + `return x.XPath('//div[@id="pages"]').Get(1).GetAttribute('data-count')`,
	"node missing attribute":     querySnippetsPrefix + `return '[' .. x.XPath('//li/a').Get(3).GetAttribute('title') .. ']'`,
	"node property":              querySnippetsPrefix + `return x.XPath('//img').Get(3).GetProperty('data-src').ToString()`,
	"node xpath string":          querySnippetsPrefix + `return x.XPath('//li').Get(1).XPathString('a/@title')`,
	"node xpath string all":      querySnippetsPrefix + `return x.XPath('//ul').Get(1).XPathStringAll('li/a')`,
	"node xpath string all, sep": querySnippetsPrefix + `return x.XPath('//ul').Get(1).XPathStringAll('li/a', '/')`,
	"node xpath":                 querySnippetsPrefix + `return tostring(x.XPath('//ul').Get(1).XPath('li').Count)`,
	"node unknown":               querySnippetsPrefix + `return tostring(x.XPath('//li').Get(1).Nope)`,

	// Free functions and constants
	"constants":           `return no_error .. net_problem .. information_not_found .. asUnknown .. asChecking .. asValid .. asInvalid`,
	"constant in a URL":   `return 'code=' .. net_problem`,
	"get between":         `return GetBetween('a[', ']', 'xa[mid]y') .. '|' .. GetBetween('<', '>', 'none')`,
	"separate left":       `return SeparateLeft('key=value', '=') .. '|' .. SeparateLeft('plain', '=')`,
	"separate right":      `return SeparateRight('key=value', '=') .. '|' .. SeparateRight('plain', '=')`,
	"trim":                `return '[' .. Trim('  a b \n') .. ']'`,
	"maybe fill host":     `return MaybeFillHost('https://s.example/a/', 'b') .. ' ' .. MaybeFillHost('https://s.example', 'https://o.example/x')`,
	"status defaults":     `return MangaInfoStatusIfPos('Completed') .. '|' .. MangaInfoStatusIfPos('Ongoing') .. '|' .. MangaInfoStatusIfPos('')`,
	"status custom lists": `return MangaInfoStatusIfPos('Em andamento', 'andamento', 'completo')`,
	"status from query":   querySnippetsPrefix + `return MangaInfoStatusIfPos(x.XPathString('//span[@class="status"]'))`,
	"sleep":               `sleep(1); sleep(0); sleep(-5); return 'slept'`,
	"trim a number":       `return Trim(12)`,
	"get between a table": `return GetBetween({}, ']', 'x')`,
}

// queryWant is what each querySnippets entry must return, or whether it must
// fail. The values were taken from the gopher-lua bindings the golua ones
// replaced, so modules see no difference.
var queryWant = map[string]struct {
	want  string
	fails bool
}{
	"constant in a URL":          {"code=1", false},
	"constants":                  {"012-1012", false},
	"document size":              {"536 536", false},
	"document to string":         {"<html>", false},
	"document tostring":          {"<html>", false},
	"document unknown":           {"nil", false},
	"from a number":              {"", false},
	"from a string":              {"hi", false},
	"get between":                {"mid|", false},
	"get between a table":        {"", true},
	"href all":                   {"/c/2,/c/1,https://cdn.example/c/0|\"Chapter 2\",\"Chapter 1\",\"Chapter 0\"", false},
	"href all, context":          {"https://cdn.example/c/0\"Chapter 0\"", false},
	"href all, one list":         {"/c/2,/c/1,https://cdn.example/c/0", false},
	"href title all":             {"Second,First,\"Chapter 0\"", false},
	"json":                       {"From JSON2", false},
	"json string all":            {"One, Two", false},
	"list count and length":      {"33", false},
	"list empty iterate":         {"00", false},
	"list get by index":          {"Chapter 2", false},
	"list get out of range":      {"nil", false},
	"list index":                 {"/c/1", false},
	"list index float":           {"/c/1", false},
	"list index zero":            {"nil", false},
	"list iterate":               {"Chapter 2;Chapter 1;Chapter 0;", false},
	"list iterate, chained":      {"3", false},
	"list loop by count":         {"SecondFirst", false},
	"maybe fill host":            {"https://s.example/a/b https://o.example/x", false},
	"node attribute":             {"3", false},
	"node missing attribute":     {"[]", false},
	"node property":              {"", false},
	"node to string":             {"Status: Completed", false},
	"node unknown":               {"nil", false},
	"node xpath":                 {"3", false},
	"node xpath string":          {"Second", false},
	"node xpath string all":      {"Chapter 2, Chapter 1, Chapter 0", false},
	"node xpath string all, sep": {"Chapter 2/Chapter 1/Chapter 0", false},
	"parse html":                 {"new", false},
	"parse html from document":   {" The  Title ", false},
	"query unknown":              {"nil", false},
	"separate left":              {"key|plain", false},
	"separate right":             {"value|plain", false},
	"sleep":                      {"slept", false},
	"status custom lists":        {"ongoing", false},
	"status defaults":            {"completed|ongoing|", false},
	"status from query":          {"completed", false},
	"trim":                       {"[a b]", false},
	"trim a number":              {"12", false},
	"xpath attribute":            {"/cover.jpg", false},
	"xpath count":                {"3", false},
	"xpath count arithmetic":     {"1", false},
	"xpath count in a URL":       {"n=3", false},
	"xpath count, context":       {"1", false},
	"xpath string":               {" The  Title ", false},
	"xpath string all":           {"Alt one, Alt two", false},
	"xpath string all, context":  {"Alt one, Alt two", false},
	"xpath string all, list":     {"2/p/1.jpg\r\n/p/2.jpg", false},
	"xpath string all, sep":      {"Alt one | Alt two", false},
	"xpath string missing":       {"[]", false},
	"xpath string, list ctx":     {"Chapter 2", false},
	"xpath string, node ctx":     {"/c/1", false},
	"xpath without expression":   {"", true},
}

func TestQuery(t *testing.T) {
	for name, src := range querySnippets {
		t.Run(name, func(t *testing.T) {
			w := queryWant[name]
			got, ok := runGoluaQuery(t, src)
			if ok == w.fails {
				t.Fatalf("ok=%v %q, want it to fail=%v", ok, got, w.fails)
			}
			if got != w.want {
				t.Errorf("got %q, want %q", got, w.want)
			}
		})
	}
}
