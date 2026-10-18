//! Percent-encoding, which modules use to put text into an address.

use percent_encoding::{AsciiSet, NON_ALPHANUMERIC, percent_encode};

use super::hex::byte_from_digits;

/// The bytes escaped in a query value, which are all bytes except letters,
/// digits and `-_.~`.
const QUERY_VALUE_ESCAPES: &AsciiSet = &NON_ALPHANUMERIC
    .remove(b'-')
    .remove(b'_')
    .remove(b'.')
    .remove(b'~');

/// The bytes escaped in a path. A path keeps the separators `/`, `;` and `,`
/// and the sub-delimiters `:@&=+$` unescaped.
const PATH_ESCAPES: &AsciiSet = &QUERY_VALUE_ESCAPES
    .remove(b'$')
    .remove(b'&')
    .remove(b'+')
    .remove(b',')
    .remove(b'/')
    .remove(b':')
    .remove(b';')
    .remove(b'=')
    .remove(b'@');

/// Percent-encodes text as one value in a query string (`EncodeURLElement`).
/// Modules use it to put a title into a search address.
///
/// A space becomes `%20`, never `+`, and every reserved character is escaped.
pub(super) fn encode_query_value(text: &[u8]) -> Vec<u8> {
    percent_encode(text, QUERY_VALUE_ESCAPES)
        .collect::<String>()
        .into_bytes()
}

/// Percent-encodes text as the path of an address (`EncodeURL`).
pub(super) fn encode_path(text: &[u8]) -> Vec<u8> {
    percent_encode(text, PATH_ESCAPES)
        .collect::<String>()
        .into_bytes()
}

/// Decodes a query string value, in which `+` stands for a space
/// (`DecodeURL`).
///
/// A `%` that is not followed by two hexadecimal digits makes the whole value
/// unreadable, so the value decodes to an empty string.
pub(super) fn decode_query_value(text: &[u8]) -> Vec<u8> {
    let mut decoded = Vec::with_capacity(text.len());
    let mut rest = text;
    while let Some((&byte, after)) = rest.split_first() {
        rest = after;
        match byte {
            b'+' => decoded.push(b' '),
            b'%' => {
                let [high, low, after_escape @ ..] = rest else {
                    return Vec::new();
                };
                let Some(escaped) = byte_from_digits(*high, *low) else {
                    return Vec::new();
                };
                decoded.push(escaped);
                rest = after_escape;
            }
            _ => decoded.push(byte),
        }
    }
    decoded
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_query_value_escapes_everything_reserved() {
        assert_eq!(encode_query_value(b"a b"), b"a%20b");
        assert_eq!(
            encode_query_value(b"one/two?x=1&y=+;~-_."),
            b"one%2Ftwo%3Fx%3D1%26y%3D%2B%3B~-_."
        );
        assert_eq!(encode_query_value("café".as_bytes()), b"caf%C3%A9");
    }

    #[test]
    fn a_path_keeps_its_separators() {
        assert_eq!(
            encode_path(b"/manga/a b/c;d,e:f@g&h=i+j$k?l#m"),
            b"/manga/a%20b/c;d,e:f@g&h=i+j$k%3Fl%23m"
        );
        assert_eq!(encode_path(b"it's (new)!"), b"it%27s%20%28new%29%21");
    }

    #[test]
    fn a_query_value_decodes_escapes_and_plus() {
        assert_eq!(decode_query_value(b"a+b%20c%2Fd"), b"a b c/d");
        assert_eq!(decode_query_value(b"%e2%9c%93"), "✓".as_bytes());
        assert_eq!(decode_query_value(b"%FF"), b"\xff");
    }

    #[test]
    fn a_broken_escape_decodes_to_nothing() {
        assert_eq!(decode_query_value(b"100%"), b"");
        assert_eq!(decode_query_value(b"%zz"), b"");
        assert_eq!(decode_query_value(b"ok%2"), b"");
    }
}
