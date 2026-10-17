use super::*;

fn check(cases: &[(&str, &str)]) {
    for (internettools, standard) in cases {
        assert_eq!(
            rewrite(internettools).unwrap().xpath,
            *standard,
            "{internettools}"
        );
    }
}

#[test]
fn dots_after_a_value_read_properties() {
    check(&[
        ("($json0).data.items", "($json0)/data/items"),
        (
            "($json0).props.pageProps.series",
            "($json0)/props/pageProps/series",
        ),
        ("(//a)[1].href", "(//a)[1]/href"),
        // A quoted name, or one XPath cannot write as a step, matches by name.
        (
            r#"($json0)."PH".slug"#,
            r#"($json0)/*[codepoint-equal(name(), "PH")]/slug"#,
        ),
        (
            "($json0).data.image.1280x1840_480.url",
            r#"($json0)/data/image/*[codepoint-equal(name(), "1280x1840_480")]/url"#,
        ),
        // Elsewhere a dot is the context item, part of a name, or a number.
        ("./a", "./a"),
        ("//data.items", "//data.items"),
        ("1.5 + .5", "1.5 + .5"),
    ]);
}

#[test]
fn calling_a_value_reads_its_members_or_one_of_them() {
    check(&[
        ("($json0).data.items()", "($json0)/data/items/*"),
        ("($json0)()[2]()", "($json0)/*[2]/*"),
        (
            "($json0).chapter.pages().url",
            "($json0)/chapter/pages/*/url",
        ),
        (
            r#"(($json0).chapters)("PH")"#,
            r#"(($json0)/chapters)/*[codepoint-equal(name(), "PH")]"#,
        ),
        ("(groups)(2)", "(groups)/*[2]"),
        // An ordinary function call is left alone.
        ("concat(a, b)", "concat(a, b)"),
    ]);
    let dynamic = rewrite("(groups)(key)").unwrap().xpath;
    assert!(
        dynamic.starts_with("(groups)/*[let $atsume_key := (key)"),
        "{dynamic}"
    );
}

#[test]
fn lookups_are_child_steps() {
    check(&[
        (
            "string-join(alt_titles?*, \", \")",
            "string-join(alt_titles/*, \", \")",
        ),
        ("authors?*?name", "authors/*/name"),
        ("poster?image", "poster/image"),
        ("($json0)?data?items?1", "($json0)/data/items/*[1]"),
        (
            "chapters?*[not(unreleased=true)]",
            "chapters/*[not(unreleased=true())]",
        ),
        // A unary lookup reads from the context item.
        ("?name", "name"),
        ("count(?*)", "count(*)"),
        // An occurrence indicator in a type is not a lookup.
        ("$a instance of xs:string?", "$a instance of xs:string?"),
    ]);
}

#[test]
fn jsoniq_functions_become_paths() {
    check(&[
        ("jn:keys(groups)", "((groups)/*/name())"),
        (
            "jn:members(result)[number=$n]/identification",
            "((result)/*)[number=$n]/identification",
        ),
        ("jn:keys(($json0))", "((($json0))/*/name())"),
    ]);
}

#[test]
fn css_selectors_become_paths() {
    check(&[
        (r#"css("div#info > h1")"#, r#"(.//div[@id = "info"]/h1)"#),
        (
            r#"css("img.cover")/@src"#,
            r#"(.//img[contains(concat(' ', normalize-space(@class), ' '), " cover ")])/@src"#,
        ),
    ]);
    assert!(rewrite("css(//a)").is_err());
}

#[test]
fn json_literals_are_values_unless_they_are_steps() {
    check(&[
        (
            "restricted_view?is_open=true",
            "restricted_view/is_open=true()",
        ),
        ("a = null", "a = \"null\""),
        ("x = false", "x = false()"),
        // A path step, a function, or text inside a string is left alone.
        ("//true", "//true"),
        ("true()", "true()"),
        ("@data-show=\"false\"", "@data-show=\"false\""),
    ]);
}

#[test]
fn strings_and_comments_are_left_alone() {
    check(&[
        ("concat('a.b?c', \"x()\")", "concat('a.b?c', \"x()\")"),
        ("'it''s'.x", "'it''s'.x"),
        ("//a (: a comment with ?x :)", "//a (: a comment with ?x :)"),
    ]);
    assert!(rewrite("'open").is_err());
}

#[test]
fn a_capitalised_name_matches_either_case() {
    check(&[
        (
            "//TotalPage",
            r#"//*[codepoint-equal(name(), "TotalPage") or codepoint-equal(name(), "totalpage")]"#,
        ),
        (
            "//div/@data-URL",
            r#"//div/@*[codepoint-equal(name(), "data-URL") or codepoint-equal(name(), "data-url")]"#,
        ),
        (
            "jn:members(result)[Number=$n]",
            r#"((result)/*)[*[codepoint-equal(name(), "Number") or codepoint-equal(name(), "number")]=$n]"#,
        ),
        // Functions, axes, prefixed names and JSON properties keep their case.
        ("Foo(1)", "Foo(1)"),
        ("xs:dateTime('x')", "xs:dateTime('x')"),
        ("($json0).pageProps", "($json0)/pageProps"),
        ("($json0)?pageProps", "($json0)/pageProps"),
    ]);
}

#[test]
fn json_calls_become_variables_and_keep_their_arguments() {
    let expression =
        r#"json(*).a, parse-json('x'), 'json(no)', myjson(1), json( //script[@id="d"] )"#;
    let rewritten = rewrite(expression).unwrap();
    assert_eq!(
        rewritten.xpath,
        "($json0)/a, parse-json('x'), 'json(no)', myjson(1), ($json1)"
    );
    assert_eq!(rewritten.json_arguments, ["*", r#" //script[@id="d"] "#]);
}

#[test]
fn json_calls_are_numbered_across_nested_rewrites() {
    let rewritten = rewrite("jn:keys(json(*).a), json(//b)").unwrap();
    assert_eq!(rewritten.xpath, "((($json0)/a)/*/name()), ($json1)");
    assert_eq!(rewritten.json_arguments, ["*", "//b"]);
}

#[test]
fn a_json_call_in_a_comment_or_a_path_step_is_not_a_call_to_evaluate() {
    let commented = rewrite("//a (: json(x) :)").unwrap();
    assert_eq!(commented.json_arguments, [] as [String; 0]);
    assert!(rewrite("//script/json(.)").is_err());
    assert!(rewrite("json(").is_err());
}
