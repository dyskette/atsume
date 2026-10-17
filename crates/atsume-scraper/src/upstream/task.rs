//! A chapter's pages, which `OnGetPageNumber` and `OnGetImageURL` write into
//! `TASK`.

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods};

use crate::links::fill_host;
use crate::model::ChapterPage;
use crate::runtime::bind::{Place, int_field, lenient, text_field};
use crate::string_list::StringList;
use crate::upstream::string_list::ListObject;

/// What `TASK` holds for the chapter being read, kept in the runtime.
#[derive(Debug, Default)]
pub(crate) struct Task {
    /// The chapter the pages belong to.
    link: String,
    /// How many pages the chapter has, when the module counts them rather
    /// than listing them.
    page_number: i64,
    current_download_chapter_ptr: i64,
    /// Each page's image address, by position. An entry may be missing or
    /// empty until `OnGetImageURL` fills it.
    page_links: StringList,
    /// Each page's own web page, which some modules read the image from.
    page_container_links: StringList,
    file_names: StringList,
    chapter_links: StringList,
    chapter_names: StringList,
}

impl Task {
    /// Returns an empty record for the chapter at `link`.
    pub(crate) fn for_chapter(link: &str) -> Self {
        Self {
            link: link.to_owned(),
            ..Self::default()
        }
    }

    /// Reports whether this record is for the chapter at `link`.
    pub(crate) fn is_for(&self, link: &str) -> bool {
        self.link == link
    }

    /// Lists the pages, with every known address resolved against
    /// `root_url`.
    ///
    /// A module may count its pages or list their web pages instead of their
    /// images. Those pages are unresolved, but only if the module can resolve
    /// them; otherwise just the images it listed count.
    pub(crate) fn pages(&self, root_url: &str, resolvable: bool) -> Vec<ChapterPage> {
        let mut count = self.page_links.items().len();
        if resolvable {
            let counted = usize::try_from(self.page_number).unwrap_or(0);
            count = count
                .max(counted)
                .max(self.page_container_links.items().len());
        }
        (0..count).map(|index| self.page(root_url, index)).collect()
    }

    /// Returns the page at `index`, resolved against `root_url` if known.
    pub(crate) fn page(&self, root_url: &str, index: usize) -> ChapterPage {
        match self.page_links.items().get(index).map(|link| link.trim()) {
            Some(link) if !link.is_empty() => ChapterPage::Image(fill_host(root_url, link)),
            _ => ChapterPage::Unresolved,
        }
    }
}

/// `TASK`. It holds nothing itself; every access reaches [`Task`].
pub(crate) struct TaskObject;

impl UserData for TaskObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "Link", |t: &mut Task| &mut t.link);
        int_field(fields, "PageNumber", |t: &mut Task| &mut t.page_number);
        int_field(fields, "CurrentDownloadChapterPtr", |t: &mut Task| {
            &mut t.current_download_chapter_ptr
        });
        let lists: [(&str, Place<Task, StringList>); 5] = [
            ("PageLinks", |t| &mut t.page_links),
            ("PageContainerLinks", |t| &mut t.page_container_links),
            ("FileNames", |t| &mut t.file_names),
            ("ChapterLinks", |t| &mut t.chapter_links),
            ("ChapterNames", |t| &mut t.chapter_names),
        ];
        for (name, place) in lists {
            fields.add_field_function_get(name, move |_, _: AnyUserData| {
                Ok(ListObject::<Task>::new(place))
            });
        }
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        lenient(methods);
    }
}

/// Sets up `TASK` in a runtime.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    lua.set_app_data(Task::default());
    lua.globals().set("TASK", TaskObject)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn task(links: &[&str], containers: usize, page_number: i64) -> Task {
        let mut task = Task::for_chapter("/c/1");
        for link in links {
            task.page_links.add(*link);
        }
        for i in 0..containers {
            task.page_container_links.add(format!("/c/1/{i}"));
        }
        task.page_number = page_number;
        task
    }

    #[test]
    fn known_pages_are_absolute_and_the_rest_unresolved() {
        let root = "https://site.example/";
        let pages = task(&["/p/1.jpg", "", "//cdn.example/3.jpg"], 0, 4).pages(root, true);
        assert_eq!(
            pages,
            [
                ChapterPage::Image("https://site.example/p/1.jpg".into()),
                ChapterPage::Unresolved,
                ChapterPage::Image("https://cdn.example/3.jpg".into()),
                ChapterPage::Unresolved,
            ]
        );
    }

    #[test]
    fn listed_containers_count_as_pages() {
        assert_eq!(
            task(&[], 3, 0).pages("https://site.example/", true).len(),
            3
        );
    }

    #[test]
    fn pages_nothing_can_resolve_are_left_out() {
        let pages = task(&["/1.jpg", "/2.jpg"], 4, 5).pages("https://site.example/", false);
        assert_eq!(pages.len(), 2);
    }
}
