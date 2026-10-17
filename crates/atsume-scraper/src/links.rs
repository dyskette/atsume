//! Puts the links that modules return into the shapes the rest of atsume uses.

use url::Url;

/// Puts a link into the shape that upstream stores, which is the shape modules
/// expect to get back later.
///
/// Upstream gives a bare identifier a leading slash. A module that returns
/// `abc` and later builds `API_URL .. '/chapter' .. URL` therefore gets
/// `/chapter/abc`. Absolute addresses and paths pass through unchanged.
pub(crate) fn normalize_link(link: &str) -> String {
    let link = link.trim();
    if link.is_empty() || link.starts_with('/') || link.contains("://") {
        return link.to_owned();
    }
    format!("/{link}")
}

/// Resolves a link against the site's address, as a browser would. A bare
/// path, or a scheme-relative link such as `//cdn.example/p.jpg`, becomes
/// absolute.
///
/// An empty link resolves to the site's address itself. A link that is
/// already absolute, or that cannot be resolved, is returned unchanged. FMD2's
/// `MaybeFillHost` behaves the same way.
pub(crate) fn resolve_link(root_url: &str, link: &str) -> String {
    if link.is_empty() {
        return root_url.to_owned();
    }
    if link.starts_with("http://") || link.starts_with("https://") {
        return link.to_owned();
    }
    Url::parse(root_url)
        .and_then(|root| root.join(link))
        .map_or_else(|_| link.to_owned(), String::from)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_link_is_resolved_against_the_site() {
        let root = "https://site.example/manga/";
        assert_eq!(
            resolve_link(root, "/p/1.jpg"),
            "https://site.example/p/1.jpg"
        );
        assert_eq!(
            resolve_link(root, "//cdn.example/p.jpg"),
            "https://cdn.example/p.jpg"
        );
        assert_eq!(
            resolve_link(root, "http://other.example/x"),
            "http://other.example/x"
        );
        assert_eq!(resolve_link(root, ""), root);
        assert_eq!(resolve_link("not a url", "/p"), "/p");
    }

    #[test]
    fn a_bare_identifier_becomes_a_path() {
        assert_eq!(normalize_link(" abc "), "/abc");
        assert_eq!(normalize_link("/manga/abc/"), "/manga/abc/");
        assert_eq!(normalize_link("https://a.example/x"), "https://a.example/x");
        assert_eq!(normalize_link(""), "");
    }
}
