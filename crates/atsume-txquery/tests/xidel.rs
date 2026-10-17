//! Compares atsume with xidel, the command-line front end of internettools,
//! the engine FMD2 evaluates module expressions with.
//!
//! Each expression runs on a page in both, and the results must match item
//! for item. The test needs a xidel binary in `ATSUME_XIDEL`; `make
//! rust-xidel` runs it, and CI pins the build. See `docs/MODULES.md`.

use std::io::Write;
use std::process::{Command, Stdio};

use atsume_txquery::{Document, Engine};
use serde_json::Value;

/// A series page shaped like the ones modules read.
const SERIES_PAGE: &str = r#"<!DOCTYPE html><html><head><title> Solo Leveling </title>
<meta property="og:image" content="/cover.jpg"></head><body>
<div class="info"><h1>Solo  Leveling</h1>
<p class="status">Status: Ongoing</p>
<p class="genres"><a href="/g/action">Action</a>, <a href="/g/fantasy">Fantasy</a></p>
<span data-id="42" class="author">Chugong</span></div>
<ul class="chapters">
<li class="chapter"><a href="/c/2" title="Second">Chapter 2</a><span class="date">2024-01-02</span></li>
<li class="chapter"><a href="/c/1" title="First">Chapter 1</a><span class="date">2024-01-01</span></li>
<li class="chapter locked"><a href="https://cdn.example/c/0">Chapter 0 &amp; prologue</a></li>
</ul>
<table><tr><td>Year</td><td>2018</td></tr></table>
<div id="pages"><img src="/p/1.jpg"><img data-src=" /p/2.jpg "></div>
<script id="data">{"props": {"title": "Solo", "pages": [{"url": "/p/1.jpg"}, {"url": "/p/2.jpg"}]}}</script>
</body></html>"#;

const SERIES_EXPRESSIONS: &[&str] = &[
    "//title",
    r#"//meta[@property="og:image"]/@content"#,
    "normalize-space(//h1)",
    r#"substring-after(//p[@class="status"], ": ")"#,
    r#"//p[@class="status"]/substring-after(., ":")"#,
    r#"//p[@class="genres"]/a"#,
    r#"string-join(//p[@class="genres"]/a, ", ")"#,
    "//li/a/@href",
    "(//li/a)[last()]",
    r#"//li[contains(@class, "locked")]/a"#,
    "count(//li)",
    "//li[2]/a/@title",
    r#"//li/span[@class="date"]/text()"#,
    r#"//td[. = "Year"]/following-sibling::td"#,
    "//table/tbody/tr/td[2]",
    "//img/@data-src",
    "normalize-space(//img[2]/@data-src)",
    r#"//span[@class="author"]/@data-id"#,
    r#"upper-case(//span[@class="author"])"#,
    "lower-case(//li[1]/a)",
    r#"replace(//h1, "\s+", " ")"#,
    r#"concat(//li[1]/a, " | ", //li[2]/a)"#,
    r#"starts-with(//li[3]/a/@href, "http")"#,
    r#"resolve-uri(//li[1]/a/@href, "https://site.example/manga/")"#,
    "//li/a[not(@title)]",
    r#"name(//*[@id = "pages"])"#,
    "//li[3]/a",
    "//li/a ! string-length(.)",
    r#"//a/@href[contains(., "/g/")]"#,
    "substring(//li[1]/span, 1, 4)",
    r#"tokenize(//li[1]/span, "-")"#,
    r#"//li[a[contains(., "Chapter 1")]]/span"#,
    r#"(//li/a/@href)[. = "/c/1"]"#,
    "exists(//li) = true",
    r#"css("ul.chapters > li.chapter > a")/@href"#,
    r#"css("li:nth-last-child(1) a")"#,
    r#"css("div#pages img")/@src"#,
    r#"json(//script[@id = "data"]).props.title"#,
    r#"json(//script[@id = "data"]).props.pages().url"#,
    "json(//script).props.pages()[2].url",
    // Upstream compares strings ignoring ASCII case.
    r#"//li/a[@title = "second"]"#,
    r#"//li/a[contains(., "chapter 1")]"#,
    r#"//li[starts-with(a, "CHAPTER")]/a/@href"#,
    r#"substring-after(//p[@class="status"], "STATUS: ")"#,
    "//LI/A",
    "//li[1]/a/@TITLE",
    r#"//li[@class = "chapter"]/a"#,
    r#"//*[@ID = "pages"]/img/@src"#,
];

/// A JSON response, which modules read through `json(*)`.
const JSON_PAGE: &str = r#"{"data": {"title": "Solo Leveling", "count": 7, "rating": 4.5,
"cover": null, "flags": {"adult": false, "ongoing": true}, "tags": ["action", "fantasy"],
"weird key": "spaced", "summary": "<p>Hunter &amp; <b>shadows</b></p>",
"chapters": [{"id": 10, "name": "One", "free": true}, {"id": 11, "name": "Two", "free": false},
{"id": 12, "name": "Three", "free": true}],
"author": {"name": "Chugong", "links": {"site": "https://chugong.example"}},
"escaped": "caf\u00e9 \ud83d\ude00 \"quoted\""}}"#;

const JSON_EXPRESSIONS: &[&str] = &[
    "json(*).data.title",
    "json(*).data.count + 1",
    "json(*).data.rating",
    r#"concat("[", json(*).data.cover, "]")"#,
    "json(*).data.flags.adult",
    "json(*).data.flags.ongoing",
    "json(*).data.tags()",
    r#"string-join(json(*).data.tags(), ", ")"#,
    "json(*).data.tags(2)",
    "json(*).data.chapters().name",
    "json(*).data.chapters()[free = true].name",
    "json(*).data.chapters()[2].id",
    "json(*).data.chapters()[id = 11].name",
    "count(json(*).data.chapters())",
    "json(*).data.chapters()[last()].name",
    "json(*)?data?chapters?*?name",
    "json(*)?data?chapters?2?name",
    "jn:keys(json(*).data.author)",
    "jn:members(json(*).data.tags)",
    r#"json(*).data."weird key""#,
    "json(*).data.summary",
    "json(*).data.author.links.site",
    r#"json(*).data.author("name")"#,
    r#"json(*)("data")("title")"#,
    "json(*).data.escaped",
    "json(*).data.nothing",
    "json(*).data.chapters()?name",
    "json(*)?data?title",
    "json(*).data.flags.adult = false",
    "json(*).data.count * 2",
    "json(*).data.rating > 4",
    r#"json(*).data.tags()[. = "fantasy"]"#,
    "string(json(*).data.cover)",
    "json(*).data.1280x720",
    r#"json(*).data.chapters()[name = "two"].id"#,
];

/// Expressions atsume still evaluates differently from internettools, with
/// the page each runs on.
const DIVERGENCES: &[(&str, &str)] = &[
    // A JSON value is an element here, so a property that is false is still
    // there, and true as a predicate.
    (JSON_PAGE, "json(*).data.chapters()[free].name"),
];

/// Restores the settings of the engine FMD2 creates, which xidel changes:
/// `true`, `false` and `null` as literals, path steps on JSON objects, and
/// string literals read without entities, as in XPath.
const FMD2_ENGINE_OPTIONS: &str = r#"declare option jn:jsoniq-boolean-and-null-literals "yes";
declare option pxp:extended-json "on";
declare option pxp:string-entities "off";
"#;

/// What evaluating an expression gave: each item's text, or a failure.
#[derive(Debug, PartialEq)]
enum Outcome {
    Items(Vec<String>),
    Failed,
}

/// Evaluates `expression` on `page` with xidel, parsing the page as HTML as
/// FMD2 does. The options need a prolog, so the expression runs as XQuery.
fn xidel(program: &str, page: &str, expression: &str) -> std::io::Result<Outcome> {
    let mut child = Command::new(program)
        .args(["-", "--input-format=html", "--output-format=json-wrapped"])
        .arg(format!("--xquery={FMD2_ENGINE_OPTIONS}{expression}"))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()?;
    if let Some(mut stdin) = child.stdin.take() {
        stdin.write_all(page.as_bytes())?;
    }
    let output = child.wait_with_output()?;
    if !output.status.success() {
        return Ok(Outcome::Failed);
    }
    let printed: Value = serde_json::from_slice(&output.stdout).map_err(std::io::Error::other)?;
    Ok(Outcome::Items(items_of(printed)))
}

/// Reads json-wrapped output: an array with one entry per input page, which
/// is `null` for no items, an array for several, and the item itself for one.
fn items_of(printed: Value) -> Vec<String> {
    let Value::Array(mut pages) = printed else {
        return Vec::new();
    };
    match pages.pop() {
        None | Some(Value::Null) => Vec::new(),
        Some(Value::Array(items)) => items.into_iter().map(text_of).collect(),
        Some(item) => vec![text_of(item)],
    }
}

fn text_of(item: Value) -> String {
    match item {
        Value::String(text) => text,
        other => other.to_string(),
    }
}

/// Evaluates `expression` on `page` with atsume.
fn atsume(engine: &Engine, page: &str, expression: &str) -> Outcome {
    let mut document = Document::parse_html(page.as_bytes());
    match document.evaluate(engine, expression, None) {
        Ok(result) => Outcome::Items(
            result
                .items()
                .iter()
                .map(|item| document.text(item))
                .collect(),
        ),
        Err(_) => Outcome::Failed,
    }
}

#[test]
#[ignore = "needs xidel in ATSUME_XIDEL"]
fn atsume_agrees_with_xidel() {
    let program = std::env::var("ATSUME_XIDEL").expect("set ATSUME_XIDEL to a xidel binary");
    let engine = Engine::new();
    let mut disagreements = Vec::new();
    for (page, expressions) in [
        (SERIES_PAGE, SERIES_EXPRESSIONS),
        (JSON_PAGE, JSON_EXPRESSIONS),
    ] {
        for expression in expressions {
            let upstream = xidel(&program, page, expression).unwrap();
            let ours = atsume(&engine, page, expression);
            if ours != upstream {
                disagreements.push(format!(
                    "{expression}\n  xidel:  {upstream:?}\n  atsume: {ours:?}"
                ));
            }
        }
    }
    assert!(disagreements.is_empty(), "{}", disagreements.join("\n"));
}

#[test]
#[ignore = "needs xidel in ATSUME_XIDEL"]
fn known_divergences_from_xidel_still_diverge() {
    let program = std::env::var("ATSUME_XIDEL").expect("set ATSUME_XIDEL to a xidel binary");
    let engine = Engine::new();
    for &(page, expression) in DIVERGENCES {
        let upstream = xidel(&program, page, expression).unwrap();
        let ours = atsume(&engine, page, expression);
        assert_ne!(
            ours, upstream,
            "{expression} now agrees; move it to its page's list"
        );
    }
}
