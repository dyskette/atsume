use super::*;

const PAGE: &str = r#"<!DOCTYPE html><html><head><title>Solo Leveling</title></head><body>
<!-- a comment upstream leaves out -->
<div class="post-title"><h1>Solo Leveling </h1></div>
<ul class="main"><li class="wp-manga-chapter"><a href="/manga/solo/2/">Chapter 2</a>
<li class="wp-manga-chapter"><a href="/manga/solo/1/">Chapter 1</a></ul>
<p>Status: Ongoing<p>Second paragraph
<P CLASS="Upper">case</P>
<img data-src=" https://cdn.example/1.jpg ">
<table><tr><td>implied tbody</td></tr></table>
<script>var pages = [1, 2];</script>
</body></html>"#;

fn page() -> Document {
    Document::parse_html(PAGE.as_bytes())
}

/// Evaluates `expression` on the test page and returns each item's text.
fn texts(expression: &str) -> Vec<String> {
    let engine = Engine::new();
    let mut page = page();
    let result = page.evaluate(&engine, expression, None).unwrap();
    result.items().iter().map(|item| page.text(item)).collect()
}

#[test]
fn paths_select_from_the_repaired_page() {
    assert_eq!(texts("//div[@class='post-title']/h1"), ["Solo Leveling "]);
    assert_eq!(
        texts("//li[@class='wp-manga-chapter']/a/@href"),
        ["/manga/solo/2/", "/manga/solo/1/"]
    );
    // Unclosed <li> and <p> are closed, as a browser would.
    assert_eq!(texts("count(//li)"), ["2"]);
    assert_eq!(texts("//p[2]"), ["Second paragraph\n"]);
    assert_eq!(texts("//table/tbody/tr/td"), ["implied tbody"]);
}

#[test]
fn names_are_lowercase_and_whitespace_is_kept() {
    assert_eq!(texts("//p[@class='Upper']"), ["case"]);
    assert_eq!(texts("//img/@data-src"), [" https://cdn.example/1.jpg "]);
    assert_eq!(texts("count(//comment())"), ["0"]);
    assert_eq!(texts("//script"), ["var pages = [1, 2];"]);
}

#[test]
fn functions_may_sit_in_a_path_step() {
    // The expression shape XPath 1.0 engines compile and silently get wrong.
    assert_eq!(texts("//li/a/substring-after(., ' ')"), ["2", "1"]);
    assert_eq!(
        texts("string-join((//title, //h1), '|')"),
        ["Solo Leveling|Solo Leveling "]
    );
    assert_eq!(texts("replace(//h1, '\\s+$', '')"), ["Solo Leveling"]);
}

#[test]
fn a_sequence_reads_as_its_items_joined_with_nothing_between() {
    let engine = Engine::new();
    let mut page = page();
    let links = page.evaluate(&engine, "//li/a", None).unwrap();
    assert_eq!(page.text_of(&links), "Chapter 2Chapter 1");
    let none = page.evaluate(&engine, "//nothing", None).unwrap();
    assert!(none.is_empty());
    assert_eq!(page.text_of(&none), "");
}

#[test]
fn an_expression_may_start_from_a_node() {
    let engine = Engine::new();
    let mut page = page();
    let items = page.evaluate(&engine, "//li", None).unwrap();
    let Item::Node(second) = items.items()[1] else {
        panic!("a list item should be a node");
    };
    let link = page.evaluate(&engine, "a/@href", Some(second)).unwrap();
    assert_eq!(page.text_of(&link), "/manga/solo/1/");
    assert_eq!(
        page.attribute(second, "class").as_deref(),
        Some("wp-manga-chapter")
    );
    assert_eq!(page.attribute(second, "missing"), None);
    assert_eq!(
        page.attribute(second, "CLASS").as_deref(),
        Some("wp-manga-chapter")
    );
}

#[test]
fn failures_say_which_kind_they_are() {
    let engine = Engine::new();
    let mut page = page();
    let invalid = page.evaluate(&engine, "//a[", None).unwrap_err();
    assert!(matches!(invalid, Error::Compile { .. }), "{invalid}");
    let failed = page.evaluate(&engine, "1 + 'a'", None).unwrap_err();
    assert!(matches!(failed, Error::Evaluate { .. }), "{failed}");
}

#[test]
fn a_panic_inside_the_engine_is_an_evaluation_error() {
    let engine = Engine::new();
    let mut page = page();
    let failed = page
        .evaluate(&engine, "round(10.3, -5000)", None)
        .unwrap_err();
    assert!(matches!(failed, Error::Evaluate { .. }), "{failed}");
}

#[test]
fn an_expression_is_compiled_once() {
    let engine = Engine::new();
    let mut page = page();
    for _ in 0..3 {
        page.evaluate(&engine, "//h1", None).unwrap();
    }
    assert_eq!(engine.compiled_count(), 1);
}

#[test]
fn invalid_utf8_is_replaced() {
    let engine = Engine::new();
    let mut page = Document::parse_html(b"<p>caf\xe9</p>");
    let text = page.evaluate(&engine, "//p", None).unwrap();
    assert_eq!(page.text_of(&text), "caf\u{fffd}");
}

const API: &str = r#"{"data": {"title": "Solo", "items": [
    {"name": "Chapter 2", "url": "/c/2", "external": null},
    {"name": "Chapter 1", "url": "/c/1", "summary": "<p>first &amp; best</p>"}
]}, "z": 1, "a": 2}"#;

/// Evaluates `expression` on a JSON response and returns each item's text.
fn json_texts(expression: &str) -> Vec<String> {
    let engine = Engine::new();
    let mut page = Document::parse_html(API.as_bytes());
    let result = page.evaluate(&engine, expression, None).unwrap();
    result.items().iter().map(|item| page.text(item)).collect()
}

#[test]
fn json_properties_are_path_steps() {
    assert_eq!(json_texts("json(*)/data/title"), ["Solo"]);
    assert_eq!(
        json_texts("json(*)/data/items/*/name"),
        ["Chapter 2", "Chapter 1"]
    );
    assert_eq!(json_texts("count(json(*)/data/items/*)"), ["2"]);
    assert_eq!(json_texts("json(*)//url"), ["/c/2", "/c/1"]);
    // Properties keep the order the response gives them.
    assert_eq!(json_texts("string-join(json(*)/(z, a), ',')"), ["1,2"]);
    assert_eq!(json_texts("json(*)/*[2]"), ["1"]);
}

#[test]
fn null_is_the_string_null_and_a_missing_key_is_nothing() {
    assert_eq!(json_texts("json(*)/data/items/*[1]/external"), ["null"]);
    assert_eq!(
        json_texts("json(*)/data/items/*[2]/external"),
        [] as [&str; 0]
    );
}

#[test]
fn markup_inside_json_strings_survives() {
    assert_eq!(
        json_texts("json(*)/data/items/*[2]/summary"),
        ["<p>first &amp; best</p>"]
    );
}

#[test]
fn json_reads_any_text_the_page_holds() {
    let engine = Engine::new();
    let html = r#"<script type="application/json">{"pages": [{"img": "1.jpg"}]}</script>
        <script>var data = {chapter: 'one', pages: [2, 3,]};</script>"#;
    let mut page = Document::parse_html(html.as_bytes());
    let mut texts = |expression: &str| {
        let result = page.evaluate(&engine, expression, None).unwrap();
        result
            .items()
            .iter()
            .map(|item| page.text(item))
            .collect::<Vec<_>>()
    };
    assert_eq!(
        texts(r#"json(//script[@type="application/json"])/pages/*/img"#),
        ["1.jpg"]
    );
    let literal = "json(substring-before(substring-after(//script[2], '= '), ';'))";
    assert_eq!(texts(&format!("{literal}/chapter")), ["one"]);
    assert_eq!(
        texts(&format!("string-join({literal}/pages/*, '|')")),
        ["2|3"]
    );
    assert_eq!(
        texts(&format!(
            "concat(json(//script[1])/pages/*/img, {literal}/chapter)"
        )),
        ["1.jpgone"]
    );
}

#[test]
fn a_json_text_is_parsed_once() {
    let engine = Engine::new();
    let mut page = Document::parse_html(API.as_bytes());
    let first = page.evaluate(&engine, "json(*)/data", None).unwrap();
    let again = page.evaluate(&engine, "json(*)/data", None).unwrap();
    assert_eq!(first, again);
    assert_eq!(page.json_trees.len(), 1);
}

#[test]
fn text_that_is_not_json_fails_naming_the_expression() {
    let engine = Engine::new();
    let mut page = page();
    let err = page.evaluate(&engine, "json(//h1)/a", None).unwrap_err();
    assert!(
        matches!(&err, Error::Evaluate { expression, .. } if expression == "//h1"),
        "{err}"
    );
    assert!(err.to_string().contains("invalid JSON"), "{err}");
}

#[test]
fn internettools_json_syntax_evaluates() {
    assert_eq!(json_texts("json(*).data.title"), ["Solo"]);
    assert_eq!(
        json_texts("json(*).data.items().name"),
        ["Chapter 2", "Chapter 1"]
    );
    assert_eq!(
        json_texts("string-join(json(*).data.items()?url, ', ')"),
        ["/c/2, /c/1"]
    );
    assert_eq!(json_texts("json(*)?data?items?2?name"), ["Chapter 1"]);
    assert_eq!(json_texts("count(json(*).data.items())"), ["2"]);
    assert_eq!(json_texts("jn:keys(json(*))"), ["data", "z", "a"]);
    assert_eq!(
        json_texts("json(*).data.items()[external = null]/name"),
        ["Chapter 2"]
    );
    assert_eq!(json_texts(r#"(json(*).data)("title")"#), ["Solo"]);
}

#[test]
fn a_listed_json_item_is_the_context_for_the_next_expression() {
    let engine = Engine::new();
    let mut page = Document::parse_html(API.as_bytes());
    let items = page
        .evaluate(&engine, "json(*).data.items()", None)
        .unwrap();
    let names: Vec<String> = items
        .items()
        .iter()
        .map(|item| {
            let Item::Node(node) = item else {
                panic!("an item should be a node")
            };
            let name = page.evaluate(&engine, "name", Some(*node)).unwrap();
            page.text_of(&name)
        })
        .collect();
    assert_eq!(names, ["Chapter 2", "Chapter 1"]);
}

#[test]
fn css_selects_from_the_page() {
    assert_eq!(
        texts(r#"css("li.wp-manga-chapter > a")/@href"#),
        ["/manga/solo/2/", "/manga/solo/1/"]
    );
    assert_eq!(texts(r#"css("div.post-title h1")"#), ["Solo Leveling "]);
}

#[test]
fn a_property_xpath_cannot_name_is_still_read() {
    let engine = Engine::new();
    let mut page = Document::parse_html(br#"{"image": {"1280x720": {"url": "/big.jpg"}}}"#);
    let url = page
        .evaluate(&engine, "json(*).image.1280x720.url", None)
        .unwrap();
    assert_eq!(page.text_of(&url), "/big.jpg");
}

/// Returns the first item of `expression`'s result, which must be a node.
fn first_node(page: &mut Document, engine: &Engine, expression: &str) -> Node {
    let result = page.evaluate(engine, expression, None).unwrap();
    let Some(&Item::Node(node)) = result.items().first() else {
        panic!("{expression} should select a node");
    };
    node
}

#[test]
fn a_property_is_a_child_named_after_it() {
    let engine = Engine::new();
    let mut page = Document::parse_html(br#"{"data": {"title": "One", "id": 7}}"#);
    let data = first_node(&mut page, &engine, "json(*).data");
    let title = page.property(data, "title").unwrap();
    assert_eq!(page.text(&Item::Node(title)), "One");
    assert_eq!(page.property(data, "missing"), None);
}

#[test]
fn nodes_of_a_replaced_page_still_read_from_it() {
    let engine = Engine::new();
    let mut page = Document::parse_html(b"<p>old</p>");
    let old = first_node(&mut page, &engine, "//p");
    page.replace_html(b"<b>new</b>");
    assert_eq!(page.text(&Item::Node(old)), "old");
    let new = page.evaluate(&engine, "//b | //p", None).unwrap();
    assert_eq!(page.text_of(&new), "new");
    let from_old = page.evaluate(&engine, "text()", Some(old)).unwrap();
    assert_eq!(page.text_of(&from_old), "old");
}

#[test]
fn capitalised_names_match_html_in_any_case_and_json_exactly() {
    let engine = Engine::new();
    let mut page = Document::parse_html(br#"<TotalPage>3</TotalPage><div data-URL="/x"></div>"#);
    let total = page.evaluate(&engine, "//TotalPage", None).unwrap();
    assert_eq!(page.text_of(&total), "3");
    let url = page.evaluate(&engine, "//DIV/@data-URL", None).unwrap();
    assert_eq!(page.text_of(&url), "/x");
    let mut json = Document::parse_html(br#"{"pageProps": {"Title": "A", "title": "B"}}"#);
    let title = json
        .evaluate(&engine, "json(*)/pageProps/Title", None)
        .unwrap();
    assert_eq!(json.text_of(&title), "AB");
}
