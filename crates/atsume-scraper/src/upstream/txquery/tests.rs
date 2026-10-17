use mlua::Lua;

use super::*;
use crate::CancelToken;
use crate::http::HttpConfig;
use crate::string_list::StringList;
use crate::upstream::string_list::ListObject;

const PAGE: &str = r#"<html><head><title> The  Title </title>
<meta property="og:image" content="/cover.jpg"></head><body>
<div class="info"><span class="status">Status: Completed</span><p class="alt">Alt one</p><p class="alt">Alt two</p></div>
<ul class="chapters">
  <li><a href="/c/2" title="Second">Chapter 2</a></li>
  <li><a href="/c/1" title="First">Chapter 1</a></li>
  <li><a href="https://cdn.example/c/0">Chapter 0</a></li>
</ul>
<div id="pages" data-count="3"><img src="/p/1.jpg"><img src="/p/2.jpg"><img data-src="/p/3.jpg"></div>
</body></html>"#;

const JSON: &str =
    r#"{"data":{"title":"From JSON","chapters":[{"id":10,"name":"One"},{"id":11,"name":"Two"}]}}"#;

/// The lists a test fills.
#[derive(Default)]
struct Lists {
    links: StringList,
    names: StringList,
}

/// Runs `source` with `CreateTXQuery`, with the page in `HTTP.Document`
/// available as `DOC`, and with the lists `LINKS` and `NAMES`. Returns what
/// `source` returns.
fn run(source: &str) -> mlua::Result<String> {
    let lua = Lua::new();
    let mut http = HttpSession::new(&HttpConfig::default(), CancelToken::new());
    http.response_body = PAGE.as_bytes().to_vec();
    lua.set_app_data(http);
    lua.set_app_data(Lists::default());
    register(&lua)?;
    let globals = lua.globals();
    globals.set(
        "DOC",
        DocumentObject::<HttpSession>::new(|h| &mut h.response_body),
    )?;
    globals.set("JSON", JSON)?;
    globals.set("LINKS", ListObject::<Lists>::new(|l| &mut l.links))?;
    globals.set("NAMES", ListObject::<Lists>::new(|l| &mut l.names))?;
    lua.load(format!("local x = CreateTXQuery(DOC)\n{source}"))
        .eval()
}

/// Checks that each `(source, expected)` returns `expected`.
fn check(cases: &[(&str, &str)]) {
    for (source, expected) in cases {
        match run(source) {
            Ok(got) => assert_eq!(got, *expected, "{source}"),
            Err(error) => panic!("{source}: {error}"),
        }
    }
}

#[test]
fn a_query_reads_text_numbers_and_the_document() {
    check(&[
        ("return CreateTXQuery('<p>hi</p>').XPathString('//p')", "hi"),
        ("return CreateTXQuery(5).XPathString('.')", "5"),
        ("return x.XPathString('//title')", " The  Title "),
        ("return CreateTXQuery().XPathCount('//p')", "0"),
        // Upstream builds no query from any other kind of value.
        ("return tostring(CreateTXQuery({}))", "nil"),
        ("return tostring(x.Nope)", "nil"),
    ]);
}

#[test]
fn xpath_string_joins_the_whole_result_with_nothing_between() {
    check(&[
        (
            r#"return x.XPathString('//meta[@property="og:image"]/@content')"#,
            "/cover.jpg",
        ),
        ("return '[' .. x.XPathString('//nothing') .. ']'", "[]"),
        (
            r#"return x.XPathString('//p[@class="alt"]')"#,
            "Alt oneAlt two",
        ),
    ]);
}

#[test]
fn a_failing_expression_reads_as_nothing() {
    check(&[
        ("return '[' .. x.XPathString('//[') .. ']'", "[]"),
        ("return x.XPathCount('//[')", "0"),
        ("return '[' .. x.XPathString() .. ']'", "[]"),
        ("return x.XPath('//[').Count", "0"),
    ]);
}

#[test]
fn xpath_string_all_joins_trimmed_values_or_fills_a_list() {
    check(&[
        (
            r#"return x.XPathStringAll('//p[@class="alt"]')"#,
            "Alt one, Alt two",
        ),
        (
            r#"return x.XPathStringAll('//p[@class="alt"]', ' | ')"#,
            "Alt one | Alt two",
        ),
        (
            "x.XPathStringAll('//img/@src', LINKS); return LINKS.Count .. LINKS.Text",
            "2/p/1.jpg\r\n/p/2.jpg",
        ),
        (
            r#"return x.XPathStringAll('p', ', ', x.XPath('//div[@class="info"]'))"#,
            "Alt one, Alt two",
        ),
        (
            r#"return x.XPathStringAll('p', x.XPath('//div[@class="info"]'))"#,
            "Alt one, Alt two",
        ),
        // Only the list form keeps empty values.
        (
            "return x.XPathStringAll('//li/a/string(@title)')",
            "Second, First",
        ),
        (
            "x.XPathStringAll('//li/a/string(@title)', LINKS); return LINKS.Count",
            "3",
        ),
    ]);
}

#[test]
fn trimming_keeps_a_no_break_space_as_pascal_does() {
    check(&[(
        "local nbsp = string.char(0xC2, 0xA0)
        local y = CreateTXQuery('<p> ' .. nbsp .. 'x </p>')
        return tostring(y.XPathStringAll('//p') == nbsp .. 'x')",
        "true",
    )]);
}

#[test]
fn xpath_count_counts_every_item() {
    check(&[
        ("return x.XPathCount('//li')", "3"),
        ("return 'n=' .. x.XPathCount('//li') // 2", "n=1"),
        (r#"return x.XPathCount('(1, "a", //li)')"#, "5"),
        ("return x.XPathCount('a', x.XPath('//li'))", "1"),
    ]);
}

#[test]
fn a_context_value_stands_for_its_first_node() {
    check(&[
        (
            "local n = x.XPath('//li').Get(2); return x.XPathString('a/@href', n)",
            "/c/1",
        ),
        ("return x.XPathString('a', x.XPath('//li'))", "Chapter 2"),
        (
            "return '[' .. x.XPathString('//title', x.XPath('//none')) .. ']'",
            "[]",
        ),
        (
            "local p = CreateTXQuery('<p><b>other</b></p>').XPath('//p')
            return x.XPathString('b', p)",
            "other",
        ),
    ]);
}

#[test]
fn href_all_fills_links_and_names() {
    check(&[
        (
            "x.XPathHREFAll('//li/a', LINKS, NAMES); return LINKS.CommaText .. '|' .. NAMES.CommaText",
            r#"/c/2,/c/1,https://cdn.example/c/0|"Chapter 2","Chapter 1","Chapter 0""#,
        ),
        (
            "x.XPathHREFAll('//li/a', LINKS); return LINKS.CommaText",
            "/c/2,/c/1,https://cdn.example/c/0",
        ),
        (
            "x.XPathHREFAll('a', LINKS, NAMES, x.XPath('//li').Get(3)); return LINKS.CommaText .. NAMES.CommaText",
            r#"https://cdn.example/c/0"Chapter 0""#,
        ),
        (
            "x.XPathHREFTitleAll('//li/a', LINKS, NAMES); return NAMES.CommaText",
            r#"Second,First,"Chapter 0""#,
        ),
        // A link with no text is named by its title.
        (
            r#"local y = CreateTXQuery('<a href="/s" title="Series"><img></a>')
            y.XPathHREFAll('//a', LINKS, NAMES); return NAMES.CommaText"#,
            "Series",
        ),
    ]);
}

#[test]
fn parse_html_replaces_the_page_but_not_values_read_from_it() {
    check(&[
        (
            "x.ParseHTML('<b>new</b>'); return x.XPathString('//b')",
            "new",
        ),
        (
            "x.ParseHTML('<b>new</b>'); x.ParseHTML(DOC); return x.XPathString('//title')",
            " The  Title ",
        ),
        (
            "x.ParseHTML(''); return x.XPathString('//title')",
            " The  Title ",
        ),
        (
            "local t = x.XPath('//title'); x.ParseHTML('<b>new</b>')
            return t.ToString() .. x.XPathString('//b')",
            " The  Title new",
        ),
    ]);
}

#[test]
fn json_reads_through_the_query() {
    check(&[
        (
            "local j = CreateTXQuery(JSON)
            return j.XPathString('json(*).data.title') .. j.XPathCount('json(*).data.chapters()')",
            "From JSON2",
        ),
        (
            "return CreateTXQuery(JSON).XPathStringAll('json(*).data.chapters().name')",
            "One, Two",
        ),
        (
            "local s = ''
            for v in CreateTXQuery(JSON).XPath('json(*).data.chapters()').Get() do
                s = s .. v.GetProperty('id').ToString() .. v.GetProperty('name').ToString()
            end
            return s",
            "10One11Two",
        ),
    ]);
}

#[test]
fn a_value_is_iterated_or_read_by_position() {
    check(&[
        ("return x.XPath('//li').Count", "3"),
        (
            "local s = '' for n in x.XPath('//li/a').Get() do s = s .. n.ToString() .. ';' end return s",
            "Chapter 2;Chapter 1;Chapter 0;",
        ),
        (
            "local n = 0 for _ in x.XPath('//none').Get() do n = n + 1 end return n .. x.XPath('//none').Count",
            "00",
        ),
        ("return x.XPath('//li/a').Get(1).ToString()", "Chapter 2"),
        // A position out of range gives an empty value instead of `nil`.
        (
            "local v = x.XPath('//li').Get(9); return v.Count .. '[' .. v.ToString() .. ']'",
            "0[]",
        ),
        (
            "local l = x.XPath('//li/a'); local s = '' for i = 1, l.Count do s = s .. l.Get(i).GetAttribute('title') end return s",
            "SecondFirst",
        ),
        ("return tostring(x.XPath('//li').Get(1).Nope)", "nil"),
    ]);
}

#[test]
fn a_value_reads_attributes_and_properties_of_its_first_node() {
    check(&[
        (
            "return x.XPath('//span').Get(1).ToString()",
            "Status: Completed",
        ),
        (
            r#"return x.XPath('//div[@id="pages"]').GetAttribute('DATA-COUNT')"#,
            "3",
        ),
        (
            "return '[' .. x.XPath('//li/a').Get(3).GetAttribute('title') .. ']'",
            "[]",
        ),
        (
            "return '[' .. x.XPath('//img').Get(3).GetProperty('data-src').ToString() .. ']'",
            "[]",
        ),
    ]);
}

#[test]
fn upstream_values_have_no_length_or_numeric_index() {
    assert!(run("return #x.XPath('//li')").is_err());
    assert!(run("return x.XPath('//li')[1].ToString()").is_err());
}
