//! Cases from the tests of internettools, the engine FMD2 evaluates module
//! expressions with.
//!
//! Each case is copied from `data/tests/xpath2_tests.pas` or
//! `data/tests/xpath3_tests.pas` of <https://github.com/benibela/internettools>
//! at commit 2724779c, and starts with its line there. internettools is
//! GPL-3.0-or-later, copyright Benito van der Zander. Only cases for what
//! FMD2's modules use are kept; `docs/MODULES.md` says how they were chosen.

use atsume_txquery::{Document, Engine};

/// A case: its line in internettools' file, the expression, and the text
/// internettools gives.
type Case = (u32, &'static str, &'static str);

/// Evaluates every case on `page` and checks the result's text.
///
/// `xpath2_tests.pas` compares the whole result's text, items joined with
/// nothing between. `xpath3_tests.pas` wraps each expression in `join()`, so
/// those cases pass a space as `separator`.
fn check(page: &str, cases: &[Case], separator: &str) {
    let engine = Engine::new();
    let mut document = Document::parse_html(page.as_bytes());
    for &(line, expression, expected) in cases {
        let got = document.evaluate(&engine, expression, None).map(|result| {
            let texts: Vec<String> = result
                .items()
                .iter()
                .map(|item| document.text(item))
                .collect();
            texts.join(separator)
        });
        assert_eq!(got, Ok(expected.to_owned()), "line {line}: {expression}");
    }
}

#[test]
fn json_reads_values_and_their_members() {
    check("", JSON, "");
}

const JSON: &[Case] = &[
    (2622, r#"json('{"a": 123}').a"#, "123"),
    (2623, r#"json('"123"')"#, "123"),
    (2624, "json('12.7')", "12.7"),
    (2625, "json('2000000000')", "2000000000"),
    (2626, "json('2000000000000000')", "2000000000000000"),
    (2627, "json('true')", "true"),
    (2628, "jn:members(json('[1, 2, 3]'))", "123"),
    (2638, "count(json('[1, 2, 3, [4, 5, 6], [7] ]'))", "1"),
    (2639, r#"json('[{"hallo": "world"}]')(1).hallo"#, "world"),
    (
        2640,
        r#"json('[{"hallo": "world"}, {hallo: 1000}]')(2).hallo"#,
        "1000",
    ),
    (
        2641,
        r#"json('{"hallo": "world"} {hallo: 1000}')[1].hallo"#,
        "world",
    ),
    (
        2642,
        r#"json('{"hallo": "world"} {hallo: 1000}')[2].hallo"#,
        "1000",
    ),
];

#[test]
fn string_functions_the_modules_call() {
    check("", STRINGS, "");
}

const STRINGS: &[Case] = &[
    (282, "concat('a','b','c')", "abc"),
    (284, "concat('hallo', ' ', 'welt') = 'hallo welt'", "true"),
    (285, "concat  (  'a',  'b',  'c'  )", "abc"),
    (286, "concat('a','b',concat('c','d'))", "abcd"),
    (287, "concat('a',concat('x','y','z'),'b','c')", "axyzbc"),
    (289, "concat('cond is ','abc'='abc')", "cond is true"),
    (290, "concat('>','123'!='test','<')", ">true<"),
    (291, "concat(concat('123','abc')='123abc','-#-')", "true-#-"),
    (292, "concat('(','abc'=concat('a','b','c'),')')", "(true)"),
    (331, r#"matches("foo|barXr", "foo \| bar\Dr", "x")"#, "true"),
    (
        337,
        r#"fn:matches("helloworld", "hello world", "x") "#,
        "true",
    ),
    (
        338,
        r#"fn:matches("helloworld", "hello[ ]world", "x") "#,
        "false",
    ),
    (
        339,
        r#"fn:matches("hello world", "hello\ sworld", "x") "#,
        "true",
    ),
    (
        340,
        r#"fn:matches("hello world", "hello world", "x") "#,
        "false",
    ),
    (363, r#"replace("abracadabra", "bra", "*")"#, "a*cada*"),
    (364, r#"replace("abracadabra", "a.*a", "*")"#, "*"),
    (365, r#"replace("abracadabra", "a.*?a", "*")"#, "*c*bra"),
    (366, r#"replace("abracadabra", "a", "")"#, "brcdbr"),
    (369, r#"replace("AAAA", "A+", "b")"#, "b"),
    (370, r#"replace("AAAA", "A+?", "b")"#, "bbbb"),
    (372, r#"replace("AAAA", "a+", "b")"#, "AAAA"),
    (373, r#"replace("AAAA", "a+", "b", 'i')"#, "b"),
    (374, r#"translate("bar","abc","ABC")"#, "BAr"),
    (376, r#"translate("--aaa--","abc-","ABC")"#, "AAA"),
    (377, r#"translate("abcdabc", "abc", "AB")"#, "ABdAB"),
    (378, r#"translate("abcdabc", "abc", "bca")"#, "bcadbca"),
    (394, r#"string-join(tokenize("a,b,c",","), ";")"#, "a;b;c"),
];

#[test]
fn a_sequence_reads_as_its_items() {
    check("", SEQUENCES, " ");
}

const SEQUENCES: &[Case] = &[
    (43, "(10,20,30) ! position()", "1 2 3"),
    (45, "( (10,20,30) ! (. + 1) ) ! position()", "1 2 3"),
    (
        46,
        r#"(10,20,30) ! (position(), ":", .)"#,
        "1 : 10 2 : 20 3 : 30",
    ),
    (47, "count((10,20,30) ! ())", "0"),
    (48, "count((10,20,30) ! (1,2))", "6"),
    (
        145,
        "fn:fold-left(1 to 5, 0, function($a, $b) { $a + $b })",
        "15",
    ),
    (
        149,
        "fn:fold-left(1 to 5, (), function($a, $b) {($b, $a)})",
        "5 4 3 2 1",
    ),
    (
        151,
        r#"fold-left(1 to 5, "$zero", fn:concat("$f(", ?, ", ", ?, ")"))"#,
        "$f($f($f($f($f($zero, 1), 2), 3), 4), 5)",
    ),
    (
        160,
        "exists(fn:function-name(function($node){count($node/*)}))",
        "false",
    ),
];

#[test]
fn css_selects_what_internettools_selects() {
    for &(page, cases) in CSS {
        check(page, cases, "");
    }
}

/// Pages and the selector cases run on each.
const CSS: &[(&str, &[Case])] = &[
    (
        r#"<x><a hreflang="en" class="warning">a1</a><a hreflang="de" id="myid">a2</a><a hreflang="fr-en-de">a3</a></x>"#,
        &[
            (3099, r#"string-join(css("a"), ",")"#, "a1,a2,a3"),
            (3100, r##"string-join(css("#myid"), ",")"##, "a2"),
            (3101, r#"string-join(css("*#myid"), ",")"#, "a2"),
            (3102, r#"string-join(css(".warning"), ",")"#, "a1"),
            (3103, r#"string-join(css("*.warning"), ",")"#, "a1"),
            (3104, r#"string-join(css("*[hreflang=en]"), ",")"#, "a1"),
            (3105, r#"string-join(css("[hreflang=en]"), ",")"#, "a1"),
        ],
    ),
    (
        r#"<x><h1>A</h1><h1 title="T">B</h1><h1 title>C</h1><span class="example">S1</span><span hello="Cleveland">S2</span><span hello="Cleveland" class="EXAMPLE">S3</span><span goodbye="Columbus">S4</span><span hello="Cleveland" goodbye="Columbus">S5</span><a rel="copyright copyleft copyeditor">A1</a><a href="http://www.w3.org/">A2</a><a rel="copyright" href="http://www.w3.org/index.html">A3</a><a rel="xyzcopyrightfoo">A4</a><DIALOGUE character="romeo">D1</DIALOGUE><DIALOGUE character="juliet">D2</DIALOGUE></x>"#,
        &[
            (3109, r#"string-join(css("h1[title]"), ",")"#, "B,C"),
            (
                3111,
                r#"string-join(css("span[hello='Cleveland'][goodbye=Columbus]"), ",")"#,
                "S5",
            ),
            (
                3112,
                r#"string-join(css('a[rel~="copyright"]'), ",")"#,
                "A1,A3",
            ),
            (
                3113,
                r#"string-join(css('a[href="http://www.w3.org/"]'), ",")"#,
                "A2",
            ),
            (
                3114,
                r#"string-join(css('DIALOGUE[character=romeo]'), ",")"#,
                "D1",
            ),
            (
                3115,
                r#"string-join(css('DIALOGUE[character=juliet]'), ",")"#,
                "D2",
            ),
        ],
    ),
    (
        r#"<a href="http://xyz.com">A1</a><a href="http://xyz.com/test.gif">A2</a><a href="http://xyz.com/test.html">A3</a>"#,
        &[(3117, r#"string-join(css('a[href$=".html"]'), ",")"#, "A3")],
    ),
    (
        r#"<p title="foobar">P1</p><p title="xyzhelloyzxy">P2</p><p title="hello">P3</p>"#,
        &[(
            3118,
            r#"string-join(css('p[title*="hello"]'), ",")"#,
            "P2,P3",
        )],
    ),
    (
        r#"<span class="pastoral">S1</span><H1>Not green</H1><H1 class="pastoral">Very green</H1><p class="pastoral blue aqua marine">P1</p><p class="pastoral blue">P2</p>"#,
        &[
            (
                3124,
                r#"string-join(css('*.pastoral'), ",")"#,
                "S1,Very green,P1,P2",
            ),
            (
                3125,
                r#"string-join(css('.pastoral'), ",")"#,
                "S1,Very green,P1,P2",
            ),
            (
                3126,
                r#"string-join(css('H1.pastoral'), ",")"#,
                "Very green",
            ),
            (3127, r#"string-join(css('p.pastoral.marine'), ",")"#, "P1"),
        ],
    ),
    (
        r#"<body><h1 id="chapter1">ha</h1><span id="z98y">S</span></body>"#,
        &[
            (3128, r#"string-join(css('h1#chapter1'), ",")"#, "ha"),
            (3130, r#"string-join(css('#chapter1'), ",")"#, "ha"),
            (3131, r#"string-join(css('*#z98y'), ",")"#, "S"),
        ],
    ),
    (
        r#"<body><h2 id="chapter1">ha</h2><span id="z98y">S</span></body>"#,
        &[
            (3132, r#"string-join(css('h1#chapter1'), ",")"#, ""),
            (3133, r#"string-join(css('#chapter1'), ",")"#, "ha"),
        ],
    ),
    (
        "<table><tr><td>1</td></tr> <tr><td>2</td></tr> <tr><td>3</td></tr> <tr><td>4</td></tr> <tr><td>5</td></tr> <tr><td>6</td></tr> <tr><td>7</td></tr> <tr><td>8</td></tr> <tr><td>9</td></tr> <tr><td>10</td></tr> <tr><td>11</td></tr> <tr><td>12</td></tr> <tr><td>13</td></tr> <tr><td>14</td></tr> <tr><td>15</td></tr> <tr><td>16</td></tr> <tr><td>17</td></tr> <tr><td>18</td></tr> <tr><td>19</td></tr> <tr><td>20</td></tr> </table>",
        &[
            (3134, r#"string-join(css('tr:nth-child(7)'), ",")"#, "7"),
            (3143, r#"string-join(css(':nth-child(5)'), ",")"#, "5"),
            (3151, r#"string-join(css('tr:nth-child( +6 )'), ",")"#, "6"),
            (
                3170,
                r#"string-join(css('tr:nth-last-child(7)'), ",")"#,
                "14",
            ),
        ],
    ),
    (
        "<div><h1>H1</h1><h1>H2</h1><span>S1</span><span>S2</span><span>S3</span><span>S4</span></div>",
        &[
            (3194, r#"string-join(css('h1:nth-child( 2 )'), ",")"#, "H2"),
            (3195, r#"string-join(css('span:nth-child( 2 )'), ",")"#, ""),
            (
                3200,
                r#"string-join(css('h1:nth-last-child( 2 )'), ",")"#,
                "",
            ),
            (
                3201,
                r#"string-join(css('span:nth-last-child( 2 )'), ",")"#,
                "S3",
            ),
            (3203, r#"string-join(css('h1:first-child'), ",")"#, "H1"),
            (3205, r#"string-join(css('h1:last-child'), ",")"#, ""),
            (3206, r#"string-join(css('span:first-child'), ",")"#, ""),
            (3207, r#"string-join(css('span:last-child'), ",")"#, "S4"),
        ],
    ),
    (
        r#"<p> The last P before the note.</p><div class="note">    <h2> Note </h2>   <p> The first P inside the note.</p></div>"#,
        &[(3225, r#"string-join(css('div > p:first-child'), ",")"#, "")],
    ),
    (
        "<x><p></p><foo>bar</foo><foo><bar>bla</bar></foo><foo>this is not <bar>:empty</bar></foo><p>1</p></x>",
        &[
            (3227, "count(css('p'))", "2"),
            (3229, "count(css('foo'))", "3"),
        ],
    ),
    (
        r#"<x><a>a1</a><p>p1</p><a href>a2</a><a href="foobar">a3</a><a>a4</a></x>"#,
        &[(3237, r#"string-join(css('a'), ",")"#, "a1,a2,a3,a4")],
    ),
    (
        r#"<h1>This <span class="myclass">headline is <em>very</em> important</span><em>a</em></h1>"#,
        &[
            (3244, r#"string-join(css('h1 em'), ",")"#, "very,a"),
            (3247, r#"string-join(css('h1 * em'), ",")"#, "very"),
            (3248, r#"string-join(css('h1 > em'), ",")"#, "a"),
        ],
    ),
    (
        r#"<div><p><a>A1</a><a href="x">A2</a>P1</p><span><p><a>A3</a><a href="qw">A4</a>P2</p></span><p>P3<a>A5</a><a href="qwq">A6</a></p></div>"#,
        &[
            (3258, r#"string-join(css('div * p'), ",")"#, "A3A4P2"),
            (
                3260,
                r#"string-join(css('div p *[href]'), ",")"#,
                "A2,A4,A6",
            ),
            (3261, r#"string-join(css('div > p *[href]'), ",")"#, "A2,A6"),
            (3262, r#"string-join(css('div>p *[href]'), ",")"#, "A2,A6"),
            (3263, r#"string-join(css('div>*>p *[href]'), ",")"#, "A4"),
        ],
    ),
    (
        "<body><p>P0</p><div><ol><li><p>P!</p></li><div><li><p>pppp</p></li></div></ol><p>P2</p></div><P>P3</p></body>",
        &[
            (3264, r#"string-join(css('body > p'), ",")"#, "P0,P3"),
            (
                3267,
                r#"string-join(css('body	p'), ",")"#,
                "P0,P!,pppp,P2,P3",
            ),
            (3268, r#"string-join(css('div ol>li p'), ",")"#, "P!"),
        ],
    ),
    (
        "<math>M0</math><p>P0</p><x><math>M1</math><p>P1</p><math>M2</math>xxxx<p>P2</p><math>M3</math></x><p>P3</p>",
        &[(3272, r#"string-join(css('math + p'), ",")"#, "P0,P1,P2")],
    ),
    (
        r#"<html><h1 class="x">h1a</h1><h2>h2aa</h2><h2>h2ab</h2><h1 class="opener">h1b</h1><h2>h2ba</h2><h2>h2bb</h2></html>"#,
        &[(3275, r#"string-join(css('h1.opener + h2'), ",")"#, "h2ba")],
    ),
    (
        r#"<html>..<h1 class="x">h1a</h1>..<h2>h2aa</h2>..<h2>h2ab</h2>..<h1 class="opener">h1b</h1>..<h2>h2ba</h2>..<h2>h2bb</h2>..</html>"#,
        &[(3278, r#"string-join(css('h1.opener + h2'), ",")"#, "h2ba")],
    ),
    (
        "<h1>Definition of the function a</h1><p>Function a(x) has to be applied to all figures in the table.</p><pre>function a(x) = 12x/13.5</pre>",
        &[
            (
                3279,
                r#"string-join(css('h1 ~ pre'), ",")"#,
                "function a(x) = 12x/13.5",
            ),
            (3282, r#"string-join(css('h1 + pre'), ",")"#, ""),
            (3283, r#"string-join(css('h1 > pre'), ",")"#, ""),
        ],
    ),
    (
        "<blockquote><div><div><p>This text should be green.</p></div></div></blockquote>",
        &[(
            3284,
            r#"string-join(css('blockquote div > p'), ",")"#,
            "This text should be green.",
        )],
    ),
    (
        r#"  <p title="hello world">This line should have a green background.</p>"#,
        &[
            (
                3288,
                r#"string-join(css('p'), ",")"#,
                "This line should have a green background.",
            ),
            (
                3290,
                r#"string-join(css('[title~="hello world"]'), ",")"#,
                "",
            ),
        ],
    ),
    (
        r#"<p class="a b c">This paragraph should have green background because CLASS contains b</p><address title="tot foo bar"><span class="a c">This address should also</span>  <span class="a bb c">have green background because the selector in the last rule does not apply to the inner SPANs.</span></address>"#,
        &[
            (
                3291,
                r#"string-join(css('p[class~="b"]'), ",")"#,
                "This paragraph should have green background because CLASS contains b",
            ),
            (3294, r#"string-join(css('span[class~="b"] '), ",")"#, ""),
        ],
    ),
    (
        "<x><a>1</a><b>2</b><c>3</c><a>1b</a><b>2b</b></x>",
        &[
            (3295, r#"string-join(css('a,c'), ",")"#, "1,3,1b"),
            (3297, r#"string-join(css('a,c,b'), ",")"#, "1,2,3,1b,2b"),
            (3298, r#"string-join(css('a , c'), ",")"#, "1,3,1b"),
            (3299, r#"string-join(css('a, c'), ",")"#, "1,3,1b"),
            (3300, r#"string-join(css('a ,c'), ",")"#, "1,3,1b"),
        ],
    ),
];

/// Cases where atsume still gives another result than internettools: the
/// line, the page, the expression, internettools' text, and atsume's.
///
/// Upstream compares strings ignoring case; xee does not yet.
const DIVERGENCES: &[(u32, &str, &str, &str, &str)] = &[
    (
        3110,
        r#"<x><h1>A</h1><h1 title="T">B</h1><h1 title>C</h1><span class="example">S1</span><span hello="Cleveland">S2</span><span hello="Cleveland" class="EXAMPLE">S3</span><span goodbye="Columbus">S4</span><span hello="Cleveland" goodbye="Columbus">S5</span><a rel="copyright copyleft copyeditor">A1</a><a href="http://www.w3.org/">A2</a><a rel="copyright" href="http://www.w3.org/index.html">A3</a><a rel="xyzcopyrightfoo">A4</a><DIALOGUE character="romeo">D1</DIALOGUE><DIALOGUE character="juliet">D2</DIALOGUE></x>"#,
        r#"string-join(css("span[class='example']"), ",")"#,
        "S1,S3",
        "S1",
    ),
    (
        3116,
        r#"<x><object type="image/123">O1</object><object type="IMAGE/">O2</object><object type="image">O3</object></x>"#,
        r#"string-join(css('object[type^="image/"]'), ",")"#,
        "O1,O2",
        "O1",
    ),
];

#[test]
fn known_divergences_still_diverge() {
    let engine = Engine::new();
    for &(line, page, expression, upstream, atsume) in DIVERGENCES {
        let mut document = Document::parse_html(page.as_bytes());
        let result = document.evaluate(&engine, expression, None).unwrap();
        let got = document.text_of(&result);
        assert_ne!(
            got, upstream,
            "line {line} now matches upstream; move it to its group"
        );
        assert_eq!(got, atsume, "line {line}: {expression}");
    }
}
