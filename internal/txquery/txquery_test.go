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
