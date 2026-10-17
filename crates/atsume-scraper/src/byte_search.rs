//! Finds text inside Lua strings, which hold bytes that need not be UTF-8.

/// Splits `text` around the first occurrence of `separator`, and returns the
/// bytes before it and the bytes after it.
///
/// An empty separator occurs at the start of any text, so the bytes before
/// it are empty. A separator that does not occur gives `None`.
pub(crate) fn split_once<'a>(text: &'a [u8], separator: &[u8]) -> Option<(&'a [u8], &'a [u8])> {
    let start = if separator.is_empty() {
        0
    } else {
        text.windows(separator.len())
            .position(|window| window == separator)?
    };
    Some((&text[..start], &text[start + separator.len()..]))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn split_once_cuts_at_the_first_occurrence() {
        assert_eq!(split_once(b"a=b=c", b"="), Some((&b"a"[..], &b"b=c"[..])));
        assert_eq!(split_once(b"key", b"key"), Some((&b""[..], &b""[..])));
        assert_eq!(split_once(b"abc", b""), Some((&b""[..], &b"abc"[..])));
        assert_eq!(split_once(b"abc", b"x"), None);
        assert_eq!(split_once(b"a", b"abc"), None);
    }
}
