package scraper

import (
	"io"
	"path/filepath"
	"testing"

	rt "github.com/arnodel/golua/runtime"
	lua "github.com/yuin/gopher-lua"
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

// runGopherQuery runs src with the gopher-lua bindings and builtins.
func runGopherQuery(t *testing.T, src string) (string, bool) {
	t.Helper()
	L := lua.NewState()
	defer L.Close()
	registerDocument(L)
	registerStrings(L)
	registerValues(L)
	registerQuery(L)
	registerNode(L)
	registerNodeList(L)
	registerBuiltins(L)
	L.SetGlobal("DOC", pushDocument(L, &Document{data: []byte(queryPage)}))
	L.SetGlobal("JSON", pushDocument(L, &Document{data: []byte(queryJSON)}))
	L.SetGlobal("LINKS", pushStrings(L, NewStrings()))
	L.SetGlobal("NAMES", pushStrings(L, NewStrings()))
	if err := L.DoString(src); err != nil {
		return "", false
	}
	return L.Get(-1).String(), true
}

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
func TestGoluaQueryMatchesGopher(t *testing.T) {
	const x = `local x = CreateTXQuery(DOC) `
	snippets := map[string]string{
		// Document
		"document to string": `return DOC.ToString():sub(1, 6)`,
		"document size":      `return tostring(DOC.Size) .. ' ' .. tostring(#DOC)`,
		"document tostring":  `return tostring(DOC):sub(1, 6)`,
		"document unknown":   `return tostring(DOC.Nope)`,

		// CreateTXQuery and the query object
		"from a string":             `return CreateTXQuery('<p>hi</p>').XPathString('//p')`,
		"from a number":             `return CreateTXQuery(5).XPathString('.')`,
		"xpath string":              x + `return x.XPathString('//title')`,
		"xpath attribute":           x + `return x.XPathString('//meta[@property="og:image"]/@content')`,
		"xpath string missing":      x + `return '[' .. x.XPathString('//nothing') .. ']'`,
		"xpath string all":          x + `return x.XPathStringAll('//p[@class="alt"]')`,
		"xpath string all, sep":     x + `return x.XPathStringAll('//p[@class="alt"]', ' | ')`,
		"xpath string all, list":    x + `x.XPathStringAll('//img/@src', LINKS); return LINKS.Count .. LINKS.Text`,
		"xpath string all, context": x + `return x.XPathStringAll('p', ', ', x.XPath('//div[@class="info"]'))`,
		"xpath count":               x + `return tostring(x.XPathCount('//li'))`,
		"xpath count in a URL":      x + `return 'n=' .. x.XPathCount('//li')`,
		"xpath count arithmetic":    x + `return tostring(x.XPathCount('//li') // 2)`,
		"xpath count, context":      x + `return tostring(x.XPathCount('a', x.XPath('//li')))`,
		"xpath string, node ctx":    x + `local n = x.XPath('//li').Get(2); return x.XPathString('a/@href', n)`,
		"xpath string, list ctx":    x + `return x.XPathString('a', x.XPath('//li'))`,
		"href all":                  x + `x.XPathHREFAll('//li/a', LINKS, NAMES); return LINKS.CommaText .. '|' .. NAMES.CommaText`,
		"href all, one list":        x + `x.XPathHREFAll('//li/a', LINKS); return LINKS.CommaText`,
		"href all, context":         x + `x.XPathHREFAll('a', LINKS, NAMES, x.XPath('//li').Get(3)); return LINKS.CommaText .. NAMES.CommaText`,
		"href title all":            x + `x.XPathHREFTitleAll('//li/a', LINKS, NAMES); return NAMES.CommaText`,
		"parse html":                x + `x.ParseHTML('<b>new</b>'); return x.XPathString('//b')`,
		"parse html from document":  x + `x.ParseHTML(DOC); return x.XPathString('//title')`,
		"json":                      `local j = CreateTXQuery(JSON); return j.XPathString('json(*).data.title') .. j.XPathCount('json(*).data.chapters()')`,
		"json string all":           `return CreateTXQuery(JSON).XPathStringAll('json(*).data.chapters().name')`,
		"query unknown":             x + `return tostring(x.Nope)`,
		"xpath without expression":  x + `return x.XPathString()`,

		// Node lists
		"list count and length": x + `local l = x.XPath('//li'); return l.Count .. #l`,
		"list iterate":          x + `local s = '' for n in x.XPath('//li/a').Get() do s = s .. n.ToString() .. ';' end return s`,
		"list iterate, chained": x + `local s = 0 for _ in CreateTXQuery(DOC).XPath('//img').Get() do s = s + 1 end return tostring(s)`,
		"list get by index":     x + `return x.XPath('//li/a').Get(1).ToString()`,
		"list get out of range": x + `return tostring(x.XPath('//li').Get(9))`,
		"list index":            x + `return x.XPath('//li/a')[2].GetAttribute('href')`,
		"list index float":      x + `return x.XPath('//li/a')[2.5].GetAttribute('href')`,
		"list index zero":       x + `return tostring(x.XPath('//li')[0])`,
		"list empty iterate":    x + `local n = 0 for _ in x.XPath('//none').Get() do n = n + 1 end return n .. x.XPath('//none').Count`,
		"list loop by count":    x + `local l = x.XPath('//li/a'); local s = '' for i = 1, l.Count do s = s .. l.Get(i).GetAttribute('title') end return s`,

		// Nodes
		"node to string":             x + `return x.XPath('//span').Get(1).ToString()`,
		"node attribute":             x + `return x.XPath('//div[@id="pages"]').Get(1).GetAttribute('data-count')`,
		"node missing attribute":     x + `return '[' .. x.XPath('//li/a').Get(3).GetAttribute('title') .. ']'`,
		"node property":              x + `return x.XPath('//img').Get(3).GetProperty('data-src').ToString()`,
		"node xpath string":          x + `return x.XPath('//li').Get(1).XPathString('a/@title')`,
		"node xpath string all":      x + `return x.XPath('//ul').Get(1).XPathStringAll('li/a')`,
		"node xpath string all, sep": x + `return x.XPath('//ul').Get(1).XPathStringAll('li/a', '/')`,
		"node xpath":                 x + `return tostring(x.XPath('//ul').Get(1).XPath('li').Count)`,
		"node unknown":               x + `return tostring(x.XPath('//li').Get(1).Nope)`,

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
		"status from query":   x + `return MangaInfoStatusIfPos(x.XPathString('//span[@class="status"]'))`,
		"sleep":               `sleep(1); sleep(0); sleep(-5); return 'slept'`,
		"trim a number":       `return Trim(12)`,
		"get between a table": `return GetBetween({}, ']', 'x')`,
	}
	for name, src := range snippets {
		t.Run(name, func(t *testing.T) {
			gotG, okG := runGopherQuery(t, src)
			gotN, okN := runGoluaQuery(t, src)
			if okG != okN {
				t.Fatalf("gopher-lua ok=%v %q, golua ok=%v %q", okG, gotG, okN, gotN)
			}
			if gotG != gotN {
				t.Errorf("gopher-lua %q\n     golua %q", gotG, gotN)
			}
		})
	}
}
