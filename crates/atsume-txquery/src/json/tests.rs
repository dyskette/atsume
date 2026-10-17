use super::*;

fn one(text: &str) -> Json {
    let mut values = parse(text).unwrap();
    assert_eq!(values.len(), 1, "{text}");
    values.remove(0)
}

fn object(properties: &[(&str, Json)]) -> Json {
    Json::Object(
        properties
            .iter()
            .map(|(k, v)| ((*k).to_owned(), v.clone()))
            .collect(),
    )
}

fn number(text: &str) -> Json {
    Json::Number(text.to_owned())
}

#[test]
fn strict_json_reads_as_written() {
    let value = one(r#"{"b": [1, "two", true, null], "a": {"c": -3.5}}"#);
    let expected = object(&[
        (
            "b",
            Json::Array(vec![
                number("1"),
                Json::String("two".into()),
                Json::Bool(true),
                Json::Null,
            ]),
        ),
        ("a", object(&[("c", number("-3.5"))])),
    ]);
    assert_eq!(value, expected);
}

#[test]
fn javascript_object_literals_are_read_too() {
    let value = one("{name: 'Solo', 'tags': ['a', 'b',], ok: TRUE, missing: Null,}");
    let expected = object(&[
        ("name", Json::String("Solo".into())),
        (
            "tags",
            Json::Array(vec![Json::String("a".into()), Json::String("b".into())]),
        ),
        ("ok", Json::Bool(true)),
        ("missing", Json::Null),
    ]);
    assert_eq!(value, expected);
}

#[test]
fn several_values_or_none_are_allowed() {
    assert_eq!(parse("{} [] 3").unwrap().len(), 3);
    assert_eq!(parse("  ").unwrap(), []);
}

#[test]
fn numbers_print_as_internettools_prints_them() {
    let read = |text: &str| one(text);
    // An integer past 2^53 keeps every digit.
    assert_eq!(
        read("123456789012345678901"),
        number("123456789012345678901")
    );
    assert_eq!(read("007"), number("7"));
    assert_eq!(read("1.50"), number("1.5"));
    assert_eq!(read(".5"), number("0.5"));
    assert_eq!(read("1e3"), number("1000"));
    assert_eq!(read("2.5e7"), number("2.5E7"));
    assert_eq!(read("NaN"), number("NaN"));
    assert_eq!(read("Infinity"), number("INF"));
}

#[test]
fn escapes_decode_including_surrogate_pairs() {
    assert_eq!(
        one(r#""a\nb \u00e9 \ud83d\ude00 \/""#),
        Json::String("a\nb \u{e9} \u{1f600} /".into())
    );
}

#[test]
fn broken_or_hostile_input_is_refused() {
    for text in [
        "{\"a\": }",
        "[1 2]",
        "{\"a\": 1",
        "\"open",
        "// comment\n{}",
    ] {
        assert!(parse(text).is_err(), "{text}");
    }
    let deep = "[".repeat(MAX_DEPTH + 1);
    assert!(parse(&deep).unwrap_err().contains("nested deeper"));
}

#[test]
fn an_unpaired_surrogate_becomes_the_replacement_character() {
    // A high surrogate followed by an ordinary escape keeps that escape.
    assert_eq!(one(r#""\ud83d\u0041""#), Json::String("\u{fffd}A".into()));
    assert_eq!(one(r#""\ud83dx""#), Json::String("\u{fffd}x".into()));
    assert_eq!(one(r#""\ude00""#), Json::String("\u{fffd}".into()));
}
