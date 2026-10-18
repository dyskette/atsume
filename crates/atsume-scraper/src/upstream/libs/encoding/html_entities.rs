//! HTML character references, which modules write to keep a response from
//! being read as markup, and read to recover text from a page.
//!
//! Modules rely on encoding most often in one pattern. They encode a JSON
//! response, pass it to `CreateTXQuery`, and read it with `json(*)`. The HTML
//! parser turns the references back into characters, so the page's text is the
//! JSON again, and a `<` inside a JSON string is not taken for a tag.

use markup5ever::data::{C1_REPLACEMENTS, NAMED_ENTITIES};

/// The longest named reference that is still recognized without its closing
/// semicolon, such as `&frac34`.
const LONGEST_NAME_WITHOUT_SEMICOLON: usize = 6;

/// Escapes the five characters that HTML gives meaning to (`HTMLEncode`).
///
/// Every other byte is kept, so the result is still UTF-8 when the input was.
pub(super) fn encode(text: &[u8]) -> Vec<u8> {
    let mut encoded = Vec::with_capacity(text.len());
    for &byte in text {
        match reference_for(byte) {
            Some(reference) => encoded.extend_from_slice(reference),
            None => encoded.push(byte),
        }
    }
    encoded
}

/// Returns the reference that stands for `byte` in encoded text.
fn reference_for(byte: u8) -> Option<&'static [u8]> {
    match byte {
        b'&' => Some(b"&amp;"),
        b'\'' => Some(b"&#39;"),
        b'<' => Some(b"&lt;"),
        b'>' => Some(b"&gt;"),
        b'"' => Some(b"&#34;"),
        _ => None,
    }
}

/// Replaces each character reference with the characters it stands for
/// (`HTMLDecode`).
///
/// A numeric reference may leave out its semicolon, and so may a legacy named
/// reference such as `&amp`. A reference that names nothing is kept as
/// written. `decode_numeric` and `decode_named` give the details.
pub(super) fn decode(text: &[u8]) -> Vec<u8> {
    let mut decoded = Vec::with_capacity(text.len());
    let mut rest = text;
    while let Some(ampersand) = rest.iter().position(|&byte| byte == b'&') {
        decoded.extend_from_slice(&rest[..ampersand]);
        let reference = &rest[ampersand..];
        let (characters, length) =
            decode_reference(reference).unwrap_or_else(|| ("&".to_owned(), 1));
        decoded.extend_from_slice(characters.as_bytes());
        rest = &reference[length..];
    }
    decoded.extend_from_slice(rest);
    decoded
}

/// Reads the reference at the start of `reference`, which begins with `&`.
/// Returns the characters it stands for and how many bytes it takes up.
fn decode_reference(reference: &[u8]) -> Option<(String, usize)> {
    if reference.get(1) == Some(&b'#') {
        let (character, length) = decode_numeric(reference)?;
        return Some((character.to_string(), length));
    }
    decode_named(reference)
}

/// Reads a numeric reference such as `&#60;` or `&#x3C;`.
///
/// The semicolon is optional. A reference shorter than four bytes, such as
/// `&#1`, is kept as written. A reference whose digits are missing or out of
/// range stands for U+FFFD.
fn decode_numeric(reference: &[u8]) -> Option<(char, usize)> {
    if reference.len() <= 3 {
        return None;
    }
    let hexadecimal = matches!(reference[2], b'x' | b'X');
    let (radix, digits_start) = if hexadecimal { (16, 3) } else { (10, 2) };
    let digits: Vec<u32> = reference[digits_start..]
        .iter()
        .map_while(|&byte| char::from(byte).to_digit(radix))
        .collect();
    let mut length = digits_start + digits.len();
    if reference.get(length) == Some(&b';') {
        length += 1;
    }
    if length <= 3 {
        return None;
    }
    let code_point = digits.iter().fold(0_u32, |value, &digit| {
        value.saturating_mul(radix).saturating_add(digit)
    });
    Some((character_for_code_point(code_point), length))
}

/// Returns the character that a numeric reference to `code_point` stands for.
///
/// Code points 0x80 to 0x9F are read as Windows-1252, as browsers read them.
/// Zero, a surrogate, or a value beyond Unicode stands for U+FFFD.
fn character_for_code_point(code_point: u32) -> char {
    let windows_1252 = code_point
        .checked_sub(0x80)
        .and_then(|offset| usize::try_from(offset).ok())
        .and_then(|index| C1_REPLACEMENTS.get(index).copied().flatten());
    if let Some(character) = windows_1252 {
        return character;
    }
    if code_point == 0 {
        return char::REPLACEMENT_CHARACTER;
    }
    char::from_u32(code_point).unwrap_or(char::REPLACEMENT_CHARACTER)
}

/// Reads a named reference such as `&amp;` or `&eacute;`.
///
/// The name is the longest run of letters and digits, with the semicolon
/// after it if there is one. When that whole name is unknown, the longest
/// known name at its start that HTML accepts without a semicolon is used, so
/// `&ampx` reads as `&x`.
fn decode_named(reference: &[u8]) -> Option<(String, usize)> {
    let letters = reference[1..]
        .iter()
        .take_while(|byte| byte.is_ascii_alphanumeric())
        .count();
    let mut end = 1 + letters;
    if reference.get(end) == Some(&b';') {
        end += 1;
    }
    let name = &reference[1..end];
    if name.is_empty() {
        return None;
    }
    if let Some(characters) = characters_named(name) {
        return Some((characters, end));
    }
    let longest_prefix = (name.len() - 1).min(LONGEST_NAME_WITHOUT_SEMICOLON);
    (2..=longest_prefix)
        .rev()
        .find_map(|length| Some((characters_named(&name[..length])?, length + 1)))
}

/// Returns the characters that the entity `name` stands for. A name of a
/// legacy entity, such as `amp`, is known with and without its semicolon.
fn characters_named(name: &[u8]) -> Option<String> {
    let name = std::str::from_utf8(name).ok()?;
    let &(first, second) = NAMED_ENTITIES.get(name)?;
    // The table also lists every prefix of a name, mapped to zero, for a
    // tokenizer's use. Those prefixes name nothing.
    if first == 0 {
        return None;
    }
    let mut characters = String::new();
    characters.push(char::from_u32(first)?);
    if second != 0 {
        characters.push(char::from_u32(second)?);
    }
    Some(characters)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn decoded(text: &str) -> String {
        String::from_utf8(decode(text.as_bytes())).unwrap()
    }

    #[test]
    fn encoding_escapes_the_five_characters_html_gives_meaning_to() {
        assert_eq!(
            encode(br#"<a href="x">Tom & Jerry's</a>"#),
            b"&lt;a href=&#34;x&#34;&gt;Tom &amp; Jerry&#39;s&lt;/a&gt;"
        );
        assert_eq!(encode("café \u{a0}".as_bytes()), "café \u{a0}".as_bytes());
        assert_eq!(encode(b"\xff&"), b"\xff&amp;");
    }

    #[test]
    fn decoding_reverses_encoding() {
        let text = r#"{"title":"a<b & 'c' > \"d\""}"#;
        assert_eq!(decode(&encode(text.as_bytes())), text.as_bytes());
    }

    #[test]
    fn decoding_reads_named_references() {
        assert_eq!(decoded("&lt;b&gt; &amp; &quot;&apos;"), "<b> & \"'");
        assert_eq!(decoded("caf&eacute; &hellip;"), "café …");
        assert_eq!(decoded("&NotEqualTilde;"), "\u{2242}\u{338}");
    }

    #[test]
    fn a_legacy_name_needs_no_semicolon() {
        assert_eq!(decoded("&amp &copy2024 &ampx;"), "& ©2024 &x;");
        assert_eq!(decoded("&notit;"), "¬it;");
    }

    #[test]
    fn decoding_reads_numeric_references() {
        assert_eq!(decoded("&#60;&#x3C;&#X3c;&#233;"), "<<<é");
        assert_eq!(decoded("&#39"), "'");
        assert_eq!(decoded("&#150;"), "\u{2013}");
        assert_eq!(decoded("&#129;"), "\u{81}");
        assert_eq!(
            decoded("&#0;&#xD800;&#99999999999;"),
            "\u{fffd}\u{fffd}\u{fffd}"
        );
    }

    /// These references sit at the edge of the four-byte minimum that
    /// `decode_numeric` applies.
    #[test]
    fn a_numeric_reference_needs_four_bytes() {
        assert_eq!(decoded("&#5x"), "&#5x");
        assert_eq!(decoded("&#5;"), "\u{5}");
        assert_eq!(decoded("&#x;"), "\u{fffd}");
        assert_eq!(decoded("&#;"), "&#;");
        assert_eq!(decoded("&#1"), "&#1");
    }

    #[test]
    fn a_reference_that_names_nothing_is_kept() {
        assert_eq!(decoded("a & b &nosuch; &; &"), "a & b &nosuch; &; &");
        assert_eq!(decode(b"\xff&lt;\xfe"), b"\xff<\xfe");
    }
}
