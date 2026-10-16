//! The shape of the links modules hand back.

/// Puts a link into the shape upstream stores, which is the shape modules
/// expect to be handed back later.
///
/// Upstream gives a bare identifier a leading slash, so a module that returns
/// `abc` and later builds `API_URL .. '/chapter' .. URL` gets `/chapter/abc`.
/// Absolute addresses and paths pass through unchanged.
pub(crate) fn normalise_link(link: &str) -> String {
    let link = link.trim();
    if link.is_empty() || link.starts_with('/') || link.contains("://") {
        return link.to_owned();
    }
    format!("/{link}")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_bare_identifier_becomes_a_path() {
        assert_eq!(normalise_link(" abc "), "/abc");
        assert_eq!(normalise_link("/manga/abc/"), "/manga/abc/");
        assert_eq!(normalise_link("https://a.example/x"), "https://a.example/x");
        assert_eq!(normalise_link(""), "");
    }
}
