use super::*;
use crate::CancelToken;
use crate::http::{HttpConfig, Response, RetryPolicy};
use crate::testing::{eval_registered, open_module_with, scripted};

/// Runs `source` with `fmd.crypto` in the global `crypto`.
fn eval(source: &str) -> mlua::Result<String> {
    eval_registered(|lua| lua.globals().set("crypto", build(lua)?), source)
}

/// Checks that each `(source, expected)` returns `expected`.
fn check(cases: &[(&str, &str)]) {
    for (source, expected) in cases {
        match eval(source) {
            Ok(got) => assert_eq!(got, *expected, "{source}"),
            Err(error) => panic!("{source}: {error}"),
        }
    }
}

#[test]
fn url_encoding_reaches_modules() {
    check(&[
        ("return crypto.EncodeURLElement('a b&c')", "a%20b%26c"),
        ("return crypto.EncodeURL('/a b/c?d')", "/a%20b/c%3Fd"),
        ("return crypto.DecodeURL('a+b%2Fc')", "a b/c"),
        ("return '[' .. crypto.DecodeURL('100%') .. ']'", "[]"),
    ]);
}

#[test]
fn html_references_reach_modules() {
    check(&[
        (
            "return crypto.HTMLEncode([[<b>\"Tom\" & 'Jerry'</b>]])",
            "&lt;b&gt;&#34;Tom&#34; &amp; &#39;Jerry&#39;&lt;/b&gt;",
        ),
        (
            "return crypto.HTMLDecode('&lt;b&gt; caf&eacute; &#39;x&#39;')",
            "<b> café 'x'",
        ),
    ]);
}

#[test]
fn base64_is_written_padded_and_read_either_way() {
    check(&[
        ("return crypto.EncodeBase64('hello')", "aGVsbG8="),
        ("return crypto.DecodeBase64('aGVsbG8=')", "hello"),
        ("return crypto.DecodeBase64('aGVsbG8')", "hello"),
        ("return crypto.DecodeBase64('aGVs\\r\\nbG8=')", "hello"),
        (
            "return '[' .. crypto.DecodeBase64('not base64!') .. ']'",
            "[]",
        ),
        (
            "local s = '\\0\\255\\128' return tostring(crypto.DecodeBase64(crypto.EncodeBase64(s)) == s)",
            "true",
        ),
    ]);
}

#[test]
fn hex_reaches_modules() {
    check(&[
        ("return crypto.StrToHex('\\0\\255A')", "00ff41"),
        ("return crypto.HexToStr('48656C6C6F')", "Hello"),
    ]);
}

#[test]
fn sha256_returns_the_digest_raw_or_as_hex() {
    let abc = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad";
    check(&[
        ("return crypto.SHA256Hex('abc')", abc),
        ("return crypto.StrToHex(crypto.SHA256('abc'))", abc),
        ("return #crypto.SHA256('abc')", "32"),
    ]);
}

#[test]
fn hmac_sha256_takes_the_data_then_the_key() {
    let fox = "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8";
    check(&[
        (
            "return crypto.HMAC_SHA256Hex('The quick brown fox jumps over the lazy dog', 'key')",
            fox,
        ),
        (
            "return crypto.StrToHex(crypto.HMAC_SHA256('The quick brown fox jumps over the lazy dog', 'key'))",
            fox,
        ),
    ]);
}

/// The vectors come from NIST SP 800-38A, section F.5.
#[test]
fn aes_ctr_matches_the_nist_vectors() {
    let run = |key: &str, ciphertext: &str| {
        let source = format!(
            "return crypto.StrToHex(crypto.AESCTR(crypto.HexToStr('6bc1bee22e409f96e93d7e117393172a'),
                crypto.HexToStr('{key}'), crypto.HexToStr('f0f1f2f3f4f5f6f7f8f9fafbfcfdfeff')))"
        );
        assert_eq!(eval(&source).unwrap(), ciphertext, "key {key}");
    };
    run(
        "2b7e151628aed2a6abf7158809cf4f3c",
        "874d6191b620e3261bef6864990db6ce",
    );
    run(
        "8e73b0f7da0e6452c810f32b809079e562f8ead2522c6b7b",
        "1abc932417521ca24f2b0459fe7e6e0b",
    );
    run(
        "603deb1015ca71be2b73aef0857d77811f352c073b6108d72d9810a30914dff4",
        "601ec313775789a5b7a7f504bbf3d228",
    );
}

#[test]
fn aes_ctr_decrypts_what_it_encrypted_across_a_counter_wrap() {
    check(&[(
        "local key, iv = string.rep('k', 16), string.rep('\\255', 16)
        local text = string.rep('long enough for three blocks ', 2)
        return tostring(crypto.AESCTR(crypto.AESCTR(text, key, iv), key, iv) == text)",
        "true",
    )]);
}

#[test]
fn aes_ctr_refuses_a_key_or_iv_of_the_wrong_length() {
    let short_key = eval("return crypto.AESCTR('x', 'short', string.rep('i', 16))").unwrap_err();
    assert!(
        short_key
            .to_string()
            .contains("fmd.crypto.AESCTR: the key is 5 bytes"),
        "{short_key}"
    );
    let short_iv = eval("return crypto.AESCTR('x', string.rep('k', 16), 'iv')").unwrap_err();
    assert!(
        short_iv.to_string().contains("the iv is 2 bytes"),
        "{short_iv}"
    );
}

#[test]
fn every_function_needs_its_arguments() {
    for source in [
        "crypto.HTMLEncode()",
        "crypto.SHA256Hex({})",
        "crypto.HMAC_SHA256('data')",
        "crypto.AESCTR('data', string.rep('k', 16))",
    ] {
        assert!(eval(source).is_err(), "{source}");
    }
    assert_eq!(eval("return crypto.StrToHex(12)").unwrap(), "3132");
}

/// A JSON response whose strings hold markup, entities, quotes and accented
/// letters, which is what makes modules encode it.
const SERIES_JSON: &str = r#"{
    "title": "Café <Noir> & \"Friends\"",
    "description": "<p>It's <b>bold</b> &amp; plain</p>",
    "chapters": [
        {"id": "c-1", "name": "Chapter 1: a < b"},
        {"id": "c-2", "name": "Chapter 2: Tom's & Jerry's"}
    ]
}"#;

/// A module that reads the series the way many FMD2 modules read an API: it
/// encodes the response with `HTMLEncode`, passes it to `CreateTXQuery`, and
/// reads it with `json(*)`.
const ENCODING_MODULE: &str = "
function GetInfo()
    local crypto = require 'fmd.crypto'
    if not HTTP.GET(MODULE.RootURL .. '/api' .. URL) then return net_problem end
    local x = CreateTXQuery(crypto.HTMLEncode(HTTP.Document.ToString()))
    MANGAINFO.Title = x.XPathString('json(*).title')
    MANGAINFO.Summary = x.XPathString('json(*).description')
    for chapter in x.XPath('json(*).chapters()').Get() do
        MANGAINFO.ChapterLinks.Add(chapter.GetProperty('id').ToString())
        MANGAINFO.ChapterNames.Add(chapter.GetProperty('name').ToString())
    end
    return no_error
end";

#[test]
fn an_html_encoded_response_reads_back_as_json_in_a_module() {
    let http = HttpConfig {
        transport: scripted(|_, _| {
            Ok(Response {
                status: 200,
                body: SERIES_JSON.as_bytes().to_vec(),
                ..Response::default()
            })
        }),
        retry: RetryPolicy::NONE,
        ..HttpConfig::default()
    };
    let init = "local m = NewWebsiteModule(); m.Name = 'Api'
        m.RootURL = 'https://api.example'; m.OnGetInfo = 'GetInfo'";
    let mut session = open_module_with(&http, CancelToken::new(), init, ENCODING_MODULE);

    let info = session.series_info("/series/1").unwrap();

    assert_eq!(info.title, r#"Café <Noir> & "Friends""#);
    assert_eq!(info.summary, "<p>It's <b>bold</b> &amp; plain</p>");
    let chapters: Vec<_> = info
        .chapters
        .iter()
        .map(|chapter| (chapter.url.as_str(), chapter.display_name()))
        .collect();
    assert_eq!(
        chapters,
        [
            ("/c-1", "Chapter 1: a < b"),
            ("/c-2", "Chapter 2: Tom's & Jerry's")
        ]
    );
}
