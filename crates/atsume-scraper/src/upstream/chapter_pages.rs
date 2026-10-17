//! A chapter's pages, which `OnGetPageNumber` and `OnGetImageURL` write into
//! `TASK`.

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods};

use crate::links::resolve_link;
use crate::model::ChapterPage;
use crate::runtime::bind::{Place, ignore_unknown_fields, int_field, text_field};
use crate::string_list::StringList;
use crate::upstream::string_list::ListObject;

/// What `TASK` holds for the chapter being read, kept in the runtime.
#[derive(Debug, Default)]
pub(crate) struct ChapterPagesRecord {
    /// The chapter the pages belong to. Modules see it as `TASK.Link`.
    chapter_url: String,
    /// How many pages the chapter has, when the module counts them rather
    /// than listing them. Modules see it as `TASK.PageNumber`.
    page_count: i64,
    /// Which chapter of the series is downloading. Modules see it as
    /// `TASK.CurrentDownloadChapterPtr`.
    downloading_chapter_index: i64,
    /// Each page's image address, by position. An entry may be missing or
    /// empty until `OnGetImageURL` fills it. Modules see it as
    /// `TASK.PageLinks`.
    page_image_urls: StringList,
    /// Each page's own web page, which some modules read the image from.
    /// Modules see it as `TASK.PageContainerLinks`.
    page_html_urls: StringList,
    file_names: StringList,
    chapter_links: StringList,
    chapter_names: StringList,
}

impl ChapterPagesRecord {
    /// Returns an empty record for the chapter at `chapter_url`.
    pub(crate) fn for_chapter(chapter_url: &str) -> Self {
        Self {
            chapter_url: chapter_url.to_owned(),
            ..Self::default()
        }
    }

    /// Reports whether this record is for the chapter at `chapter_url`.
    pub(crate) fn is_for(&self, chapter_url: &str) -> bool {
        self.chapter_url == chapter_url
    }

    /// Lists the pages, with every known address resolved against
    /// `root_url`.
    ///
    /// A module may count its pages or list their web pages instead of their
    /// images. Those pages are unresolved, but only if the module can resolve
    /// them; otherwise just the images it listed count.
    pub(crate) fn pages(&self, root_url: &str, resolvable: bool) -> Vec<ChapterPage> {
        let mut count = self.page_image_urls.items().len();
        if resolvable {
            let counted = usize::try_from(self.page_count).unwrap_or(0);
            count = count.max(counted).max(self.page_html_urls.items().len());
        }
        (0..count).map(|index| self.page(root_url, index)).collect()
    }

    /// Returns the page at `index`, resolved against `root_url` if known.
    pub(crate) fn page(&self, root_url: &str, index: usize) -> ChapterPage {
        match self
            .page_image_urls
            .items()
            .get(index)
            .map(|link| link.trim())
        {
            Some(link) if !link.is_empty() => ChapterPage::ImageUrl(resolve_link(root_url, link)),
            _ => ChapterPage::Unresolved,
        }
    }
}

/// `TASK`. It holds nothing itself; every access reaches [`ChapterPagesRecord`].
pub(crate) struct ChapterPagesObject;

impl UserData for ChapterPagesObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "Link", |t: &mut ChapterPagesRecord| {
            &mut t.chapter_url
        });
        int_field(fields, "PageNumber", |t: &mut ChapterPagesRecord| {
            &mut t.page_count
        });
        int_field(
            fields,
            "CurrentDownloadChapterPtr",
            |t: &mut ChapterPagesRecord| &mut t.downloading_chapter_index,
        );
        let lists: [(&str, Place<ChapterPagesRecord, StringList>); 5] = [
            ("PageLinks", |t| &mut t.page_image_urls),
            ("PageContainerLinks", |t| &mut t.page_html_urls),
            ("FileNames", |t| &mut t.file_names),
            ("ChapterLinks", |t| &mut t.chapter_links),
            ("ChapterNames", |t| &mut t.chapter_names),
        ];
        for (name, place) in lists {
            fields.add_field_function_get(name, move |_, _: AnyUserData| {
                Ok(ListObject::<ChapterPagesRecord>::new(place))
            });
        }
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        ignore_unknown_fields(methods);
    }
}

/// Sets the global `TASK`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    lua.globals().set("TASK", ChapterPagesObject)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn record(links: &[&str], containers: usize, page_count: i64) -> ChapterPagesRecord {
        let mut record = ChapterPagesRecord::for_chapter("/c/1");
        for link in links {
            record.page_image_urls.add(*link);
        }
        for i in 0..containers {
            record.page_html_urls.add(format!("/c/1/{i}"));
        }
        record.page_count = page_count;
        record
    }

    #[test]
    fn known_pages_are_absolute_and_the_rest_unresolved() {
        let root = "https://site.example/";
        let pages = record(&["/p/1.jpg", "", "//cdn.example/3.jpg"], 0, 4).pages(root, true);
        assert_eq!(
            pages,
            [
                ChapterPage::ImageUrl("https://site.example/p/1.jpg".into()),
                ChapterPage::Unresolved,
                ChapterPage::ImageUrl("https://cdn.example/3.jpg".into()),
                ChapterPage::Unresolved,
            ]
        );
    }

    #[test]
    fn listed_containers_count_as_pages() {
        assert_eq!(
            record(&[], 3, 0).pages("https://site.example/", true).len(),
            3
        );
    }

    #[test]
    fn pages_nothing_can_resolve_are_left_out() {
        let pages = record(&["/1.jpg", "/2.jpg"], 4, 5).pages("https://site.example/", false);
        assert_eq!(pages.len(), 2);
    }
}
