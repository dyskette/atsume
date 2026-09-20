package txquery

import "testing"

const pageHTML = `<html><body>
<div class="post-title"><h1>Solo Leveling</h1></div>
<div class="summary_image"><img data-src="https://cdn.example/cover.jpg"></div>
<div class="author-content"><a>Chugong</a><a>h-goon</a></div>
<p>Author: Chugong</p>
<div id="info"><h1>Info Heading</h1></div>
<div itemprop="description">   spaced   out   text   </div>
<li><b>Alternative(s):</b> Na Honjaman Level Up</li>
<div class="summary__content"><p>First para.</p><p>Second para.</p></div>
<div class="tags"><a>Action</a><a>Fantasy</a></div>
<ul class="chapters"><li><a href="/c/1">Ch. 1</a></li><li><a href="/c/2">Ch. 2</a></li></ul>
</body></html>`

const apiJSON = `{
  "results": {"image_server": "https://img.example/", "total_page": 12},
  "data": {"chapters": [{"id": 1, "title": "One"}, {"id": 2, "title": "Two"}]},
  "authors": [{"name": "Chugong"}, {"name": "h-goon"}],
  "genres": [{"name": "Action"}, {"name": "Fantasy"}],
  "pages": [{"path": "/p1.jpg"}, {"path": "/p2.jpg"}, {"path": "/p3.jpg"}],
  "sources": [{"images": ["a.jpg", "b.jpg"]}]
}`

// Every expression below is taken verbatim from the FMD2 corpus.
func TestXPathString(t *testing.T) {
	q, err := ParseString(pageHTML)
	if err != nil {
		t.Fatal(err)
	}
	j, err := ParseString(apiJSON)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name, expr, want string
		doc              *Query
	}{
		{"html/title", `//div[@class="post-title"]/*[self::h1 or self::h3]/text()`, "Solo Leveling", q},
		{"html/cover", `//div[@class="summary_image"]//img/@data-src`, "https://cdn.example/cover.jpg", q},
		{"css/selector", `css("div#info > h1")`, "Info Heading", q},
		// XPathString does not trim: EvalString is Eval(...).toString.
		{"funcstep/substring-after", `//p[contains(., "Author")]/substring-after(., ":")`, " Chugong", q},
		{"funcstep/normalize-space", `//div[@itemprop="description"]/normalize-space(.)`, "spaced out text", q},
		{"funcstep/alt-titles", `//li[contains(b, "Alternative(s):")]/substring-after(., "Alternative(s):")`, " Na Honjaman Level Up", q},
		// Madara builds MANGAINFO.Summary this way, so the sequence must collapse.
		{"string-join/summary", `string-join((//div[contains(@class, "summary__content")])[1]//p, "\r\n")`, `First para.\r\nSecond para.`, q},
		{"joinstep", `//div[@class="tags"]/string-join(./*,", ")`, "Action, Fantasy", q},

		{"json/nested", `json(*).results.image_server`, "https://img.example/", j},
		{"json/number", `json(*).results.total_page`, "12", j},
		{"lookup/join", `string-join(genres?*?name, ", ")`, "Action, Fantasy", j},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.doc.XPathString(c.expr); got != c.want {
				out, _ := Rewrite(c.expr)
				t.Errorf("expr %q\n rewritten %q\n got  %q\n want %q", c.expr, out, got, c.want)
			}
		})
	}
}

func TestXPathStringAll(t *testing.T) {
	q, _ := ParseString(pageHTML)
	j, _ := ParseString(apiJSON)

	cases := []struct {
		name, expr, sep, want string
		doc                   *Query
	}{
		{"html/authors", `//div[@class="author-content"]/a`, ", ", "Chugong, h-goon", q},
		{"sequence", `(//div[@class="post-title"]/h1, //div[@id="info"]/h1)`, "|", "Solo Leveling|Info Heading", q},
		{"json/array-prop", `json(*).data.chapters().title`, ",", "One,Two", j},
		{"lookup/star-name", `authors?*?name`, ", ", "Chugong, h-goon", j},
		{"lookup/pages", `pages?*?path`, ",", "/p1.jpg,/p2.jpg,/p3.jpg", j},
		{"json/indexed", `json(*).sources()[1].images()`, ",", "a.jpg,b.jpg", j},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.doc.XPathStringAll(c.expr, c.sep); got != c.want {
				out, _ := Rewrite(c.expr)
				t.Errorf("expr %q\n rewritten %q\n got  %q\n want %q", c.expr, out, got, c.want)
			}
		})
	}
}

// TestDefaultSeparator pins the ", " default from AddSeparatorString.
func TestDefaultSeparator(t *testing.T) {
	q, _ := ParseString(pageHTML)
	if got := q.XPathStringAll(`//div[@class="author-content"]/a`); got != "Chugong, h-goon" {
		t.Errorf("got %q", got)
	}
}

func TestXPathCountAndHREF(t *testing.T) {
	q, _ := ParseString(pageHTML)
	if n := q.XPathCount(`//ul[@class="chapters"]/li/a`); n != 2 {
		t.Errorf("count = %d, want 2", n)
	}
	links, names := q.XPathHREFAll(`//ul[@class="chapters"]/li/a`)
	if len(links) != 2 || links[0] != "/c/1" || names[1] != "Ch. 2" {
		t.Errorf("links=%v names=%v", links, names)
	}
}

// TestJSONFallback covers a module running a json() expression against a body
// that turned out not to be JSON: it must come up empty, not panic.
func TestJSONFallback(t *testing.T) {
	q, _ := ParseString(pageHTML)
	if got := q.XPathString(`json(*).data.title`); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestAttributeNodes covers iterating an attribute set, as in `img/@uid`.
//
// antchfx reports an attribute match as its owning element, so without special
// handling a module reading the node's text gets the element's inner text —
// empty for a void element like <img> — instead of the attribute value.
func TestAttributeNodes(t *testing.T) {
	q, err := ParseString(`<div id="p"><img uid="a/001.webp"><img uid="a/002.webp"></div>`)
	if err != nil {
		t.Fatal(err)
	}
	nodes := q.XPath(`//div[@id="p"]/img/@uid`)
	if len(nodes) != 2 {
		t.Fatalf("got %d nodes, want 2", len(nodes))
	}
	for i, want := range []string{"a/001.webp", "a/002.webp"} {
		if got := nodes[i].Text(); got != want {
			t.Errorf("[%d] Text() = %q, want %q", i, got, want)
		}
	}
}

// TestMixedSequence covers a sequence constructor whose members are not all
// node-sets.
//
// XPath 1.0 unions take node-sets only, so folding `(//a, concat(…))` into a
// union silently drops the string member — a genre list quietly missing its
// last entry, with no error.
func TestMixedSequence(t *testing.T) {
	const html = `<body>
		<a href="/genre/action"><span>Action</span></a>
		<a href="/genre/fantasy"><span>Fantasy</span></a>
		<div class="g"><div><span>Type</span></div><div>MANHWA</div></div>
	</body>`
	q, err := ParseString(html)
	if err != nil {
		t.Fatal(err)
	}
	// Taken from templates/KeyoApp.lua.
	const expr = `(//a[contains(@href, "genre")]/span, ` +
		`concat(upper-case(substring(//div[./div/span="Type"]/div[2], 1, 1)), ` +
		`lower-case(substring(//div[./div/span="Type"]/div[2], 2))))`

	if got, want := q.XPathStringAll(expr), "Action, Fantasy, Manhwa"; got != want {
		out, _ := Rewrite(expr)
		t.Errorf("got %q, want %q\n  rewritten: %s", got, want, out)
	}
}

// TestAllNodeSetSequenceStaysAUnion checks the common case is not disturbed:
// when every member is a node-set the union is still used, preserving document
// order.
func TestAllNodeSetSequenceStaysAUnion(t *testing.T) {
	q, err := ParseString(`<body><h1>First</h1><h2>Second</h2></body>`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := q.XPathStringAll(`(//h1, //h2)`, "|"), "First|Second"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestJSONFromEncodedDocument covers a body that was HTML-encoded before being
// handed to CreateTXQuery.
//
// Modules do that so a `<` inside a JSON string is not parsed as markup, which
// means the raw bytes are no longer valid JSON and the entities have to be
// decoded before json() can read them.
func TestJSONFromEncodedDocument(t *testing.T) {
	// What crypto.HTMLEncode produces for {"result":{"list":[{"name":"a<b"}]}}
	const encoded = `{&quot;result&quot;:{&quot;list&quot;:[{&quot;name&quot;:&quot;a&lt;b&quot;}]}}`
	q, err := ParseString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := q.XPathString(`json(*).result.list().name`), "a<b"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestPlainJSONStillParses guards the common case against the fallback above.
func TestPlainJSONStillParses(t *testing.T) {
	q, err := ParseString(`{"result":{"list":[{"name":"plain"}]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := q.XPathString(`json(*).result.list().name`), "plain"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestAttributeNameCaseFolding covers an XPath naming an attribute in mixed
// case.
//
// Go's HTML parser lowercases attribute names as the specification requires, so
// a case-sensitive match against `@data-URL` finds nothing. internettools folds
// the case for HTML, and modules are written assuming it — WebToons reads every
// page URL through `@data-URL`, and without folding a chapter comes back with
// no pages and no error.
func TestAttributeNameCaseFolding(t *testing.T) {
	q, err := ParseString(`<div id="_imageList"><img class="_images" data-url="/01.jpg"><img class="_images" data-url="/02.jpg"></div>`)
	if err != nil {
		t.Fatal(err)
	}
	got := q.XPathValues(`//div[@id="_imageList"]/img[@class="_images"]/@data-URL`, nil)
	if len(got) != 2 || got[0] != "/01.jpg" || got[1] != "/02.jpg" {
		t.Errorf("got %v, want the two page URLs", got)
	}
}

// TestPropertyReturnsNode covers GetProperty's contract: modules chain into
// .ToString() on the result, so a missing property must still be a node.
func TestPropertyReturnsNode(t *testing.T) {
	q, err := ParseString(`{"items":[{"title":"One"},{"other":2}]}`)
	if err != nil {
		t.Fatal(err)
	}
	nodes := q.XPath(`json(*).items()`)
	if len(nodes) != 2 {
		t.Fatalf("got %d items, want 2", len(nodes))
	}
	if got := nodes[0].Property("title").Text(); got != "One" {
		t.Errorf("present property = %q, want One", got)
	}
	if got := nodes[1].Property("title").Text(); got != "" {
		t.Errorf("absent property = %q, want empty", got)
	}
}
