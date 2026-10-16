//! A page of a site's directory, which `OnGetNameAndLink` writes into `LINKS`
//! and `NAMES`, reporting progress through `UPDATELIST`.

use mlua::{Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::bind::{int_field, lenient, state};
use crate::convert::string_arg;
use crate::links::normalise_link;
use crate::strings::{ListHandle, Strings};

/// One page of a site's directory.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DirectoryPage {
    /// The series on the page, in the order the module listed them.
    pub entries: Vec<DirectoryEntry>,
    /// How many pages the directory has, if the module said while reading
    /// this one. Some modules say so here rather than in
    /// [`Session::directory_pages`](crate::Session::directory_pages).
    pub reported_pages: Option<u32>,
}

/// One series in a site's directory.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirectoryEntry {
    /// The link to read the series from with [`Session::info`](crate::Session::info).
    pub url: String,
    /// The title, unless the module gave none.
    pub name: Option<String>,
}

impl DirectoryEntry {
    /// Returns the name to show: the entry's title, or its link if it has
    /// none.
    #[must_use]
    pub fn display_name(&self) -> &str {
        self.name.as_deref().unwrap_or(&self.url)
    }
}

/// What `LINKS`, `NAMES` and `UPDATELIST` hold while a directory page is
/// read, kept in the runtime.
#[derive(Debug, Default)]
pub(crate) struct Directory {
    links: Strings,
    names: Strings,
    /// `UPDATELIST.CurrentDirectoryPageNumber`: the page count, if reported.
    reported_pages: i64,
}

impl Directory {
    /// Reads what the module listed into a [`DirectoryPage`].
    ///
    /// Each link is paired with the name at the same position, put in the
    /// shape the module expects back, and dropped if empty.
    pub(crate) fn into_page(self) -> DirectoryPage {
        let names = self.names.items();
        let entries = self
            .links
            .items()
            .iter()
            .enumerate()
            .map(|(i, link)| DirectoryEntry {
                url: normalise_link(link),
                name: names
                    .get(i)
                    .map(|n| n.trim())
                    .filter(|n| !n.is_empty())
                    .map(str::to_owned),
            })
            .filter(|entry| !entry.url.is_empty())
            .collect();
        let reported_pages = u32::try_from(self.reported_pages).ok().filter(|&n| n > 0);
        DirectoryPage {
            entries,
            reported_pages,
        }
    }
}

/// Receives a progress message a module reports.
pub(crate) type StatusReport = Box<dyn FnMut(&str)>;

/// Where `UPDATELIST.UpdateStatusText` messages go, kept in the runtime.
#[derive(Default)]
pub(crate) struct StatusSink(pub(crate) Option<StatusReport>);

/// `UPDATELIST`. It holds nothing itself; every access reaches [`Directory`].
struct UpdateListObject;

impl UserData for UpdateListObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        int_field(fields, "CurrentDirectoryPageNumber", |d: &mut Directory| {
            &mut d.reported_pages
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_function("UpdateStatusText", |lua, text: Value| {
            let text = string_arg(lua, &text, 1)?;
            if let Some(report) = state::<StatusSink>(lua)?.0.as_mut() {
                report(&text);
            }
            Ok(())
        });
        lenient(methods);
    }
}

/// Sets up `LINKS`, `NAMES` and `UPDATELIST` in a runtime.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    lua.set_app_data(Directory::default());
    lua.set_app_data(StatusSink::default());
    let globals = lua.globals();
    globals.set("LINKS", ListHandle::<Directory>::new(|d| &mut d.links))?;
    globals.set("NAMES", ListHandle::<Directory>::new(|d| &mut d.names))?;
    globals.set("UPDATELIST", UpdateListObject)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn entries_pair_links_with_names() {
        let mut directory = Directory::default();
        for link in ["manga/a", "", "/manga/c"] {
            directory.links.add(link);
        }
        directory.names.add("A");
        directory.reported_pages = 12;
        let page = directory.into_page();
        let entries: Vec<_> = page
            .entries
            .iter()
            .map(|e| (e.url.as_str(), e.display_name()))
            .collect();
        assert_eq!(entries, [("/manga/a", "A"), ("/manga/c", "/manga/c")]);
        assert_eq!(page.reported_pages, Some(12));
        assert_eq!(Directory::default().into_page().reported_pages, None);
    }
}
