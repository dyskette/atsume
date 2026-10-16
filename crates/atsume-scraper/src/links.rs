//! The shape of the links modules hand back.

use url::Url;

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

/// Resolves a link against the site's address, as a browser would, so a bare
/// path or a scheme-relative `//cdn.example/p.jpg` becomes absolute.
///
/// An empty link is the site's address itself. A link that is already
/// absolute, or that cannot be resolved, is returned as it is.
pub(crate) fn fill_host(root_url: &str, link: &str) -> String {
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
        assert_eq!(fill_host(root, "/p/1.jpg"), "https://site.example/p/1.jpg");
        assert_eq!(
            fill_host(root, "//cdn.example/p.jpg"),
            "https://cdn.example/p.jpg"
        );
        assert_eq!(
            fill_host(root, "http://other.example/x"),
            "http://other.example/x"
        );
        assert_eq!(fill_host(root, ""), root);
        assert_eq!(fill_host("not a url", "/p"), "/p");
    }

    #[test]
    fn a_bare_identifier_becomes_a_path() {
        assert_eq!(normalise_link(" abc "), "/abc");
        assert_eq!(normalise_link("/manga/abc/"), "/manga/abc/");
        assert_eq!(normalise_link("https://a.example/x"), "https://a.example/x");
        assert_eq!(normalise_link(""), "");
    }
}
