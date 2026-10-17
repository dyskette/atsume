//! A page of a site's directory, which `OnGetNameAndLink` writes into `LINKS`
//! and `NAMES`, reporting progress through `UPDATELIST`.

use mlua::{Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::links::normalize_link;
use crate::model::{DirectoryEntry, DirectoryPage};
use crate::runtime::bind::{borrow_state, ignore_unknown_fields, int_field};
use crate::runtime::convert::text_arg;
use crate::string_list::StringList;
use crate::upstream::string_list::ListObject;

/// What `LINKS`, `NAMES` and `UPDATELIST` hold while a directory page is
/// read, kept in the runtime.
#[derive(Debug, Default)]
pub(crate) struct DirectoryPageRecord {
    links: StringList,
    names: StringList,
    /// How many pages the directory has, if the module reported it. Modules
    /// see it as `UPDATELIST.CurrentDirectoryPageNumber`.
    reported_page_count: i64,
}

impl DirectoryPageRecord {
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
                url: normalize_link(link),
                name: names
                    .get(i)
                    .map(|n| n.trim())
                    .filter(|n| !n.is_empty())
                    .map(str::to_owned),
            })
            .filter(|entry| !entry.url.is_empty())
            .collect();
        let reported_page_count = u32::try_from(self.reported_page_count)
            .ok()
            .filter(|&n| n > 0);
        DirectoryPage {
            entries,
            reported_page_count,
        }
    }
}

/// Receives a progress message a module reports.
pub(crate) type StatusReport = Box<dyn FnMut(&str)>;

/// Where `UPDATELIST.UpdateStatusText` messages go, kept in the runtime.
#[derive(Default)]
pub(crate) struct StatusSink(pub(crate) Option<StatusReport>);

/// `UPDATELIST`. It holds nothing itself; every access reaches [`DirectoryPageRecord`].
struct UpdateListObject;

impl UserData for UpdateListObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        int_field(
            fields,
            "CurrentDirectoryPageNumber",
            |d: &mut DirectoryPageRecord| &mut d.reported_page_count,
        );
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_function("UpdateStatusText", |lua, text: Value| {
            let text = text_arg(lua, &text, 1)?;
            if let Some(report) = borrow_state::<StatusSink>(lua)?.0.as_mut() {
                report(&text);
            }
            Ok(())
        });
        ignore_unknown_fields(methods);
    }
}

/// Sets the globals `LINKS`, `NAMES` and `UPDATELIST`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let globals = lua.globals();
    globals.set(
        "LINKS",
        ListObject::<DirectoryPageRecord>::new(|d| &mut d.links),
    )?;
    globals.set(
        "NAMES",
        ListObject::<DirectoryPageRecord>::new(|d| &mut d.names),
    )?;
    globals.set("UPDATELIST", UpdateListObject)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn entries_pair_links_with_names() {
        let mut directory = DirectoryPageRecord::default();
        for link in ["manga/a", "", "/manga/c"] {
            directory.links.add(link);
        }
        directory.names.add("A");
        directory.reported_page_count = 12;
        let page = directory.into_page();
        let entries: Vec<_> = page
            .entries
            .iter()
            .map(|e| (e.url.as_str(), e.display_name()))
            .collect();
        assert_eq!(entries, [("/manga/a", "A"), ("/manga/c", "/manga/c")]);
        assert_eq!(page.reported_page_count, Some(12));
        assert_eq!(
            DirectoryPageRecord::default()
                .into_page()
                .reported_page_count,
            None
        );
    }
}
