//! `MaybeFillHost`, which turns a link read from a page into an address that
//! a module can request.

use mlua::{Lua, Value};

use crate::links::resolve_link;
use crate::runtime::convert::text_arg;

/// Sets the global `MaybeFillHost(root_url, link)`.
///
/// It resolves the link against the site's address as a browser would, so a
/// path or a scheme-relative link such as `//cdn.example/p.jpg` becomes
/// absolute.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let maybe_fill_host = lua.create_function(|lua, (root_url, link): (Value, Value)| {
        let root_url = text_arg(lua, &root_url, 1)?;
        let link = text_arg(lua, &link, 2)?;
        Ok(resolve_link(&root_url, &link))
    })?;
    lua.globals().set("MaybeFillHost", maybe_fill_host)
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn fill(root_url: &str, link: &str) -> String {
        let source = format!("return MaybeFillHost('{root_url}', '{link}')");
        eval_registered(super::register, &source).unwrap()
    }

    #[test]
    fn a_path_is_resolved_against_the_site() {
        assert_eq!(fill("https://s.example/a/", "b"), "https://s.example/a/b");
        assert_eq!(
            fill("https://fanfox.net", "/manga/x/"),
            "https://fanfox.net/manga/x/"
        );
        assert_eq!(
            fill("https://fanfox.net/directory/", "2.html"),
            "https://fanfox.net/directory/2.html"
        );
        assert_eq!(fill("https://s.example/", ""), "https://s.example/");
    }

    /// A scheme-relative link borrows the scheme of the page it is on. The site
    /// `FanFox` hands back its images in that form.
    #[test]
    fn a_scheme_relative_link_takes_the_site_scheme() {
        assert_eq!(
            fill("https://fanfox.net", "//zjcdn.example/p.jpg"),
            "https://zjcdn.example/p.jpg"
        );
        assert_eq!(
            fill("http://plain.example", "//cdn.example/p.jpg"),
            "http://cdn.example/p.jpg"
        );
    }

    #[test]
    fn an_absolute_link_is_returned_unchanged() {
        assert_eq!(
            fill("https://s.example", "https://o.example/x"),
            "https://o.example/x"
        );
    }

    #[test]
    fn both_arguments_are_needed() {
        assert!(
            eval_registered(super::register, "return MaybeFillHost('https://s.example')").is_err()
        );
    }
}
