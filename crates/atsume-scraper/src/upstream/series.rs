//! A series' details, which `OnGetInfo` writes into `MANGAINFO`.

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::links::normalize_link;
use crate::model::{Chapter, SeriesInfo, SeriesStatus};
use crate::runtime::bind::{ignore_unknown_fields, text_field};
use crate::runtime::convert::{lua_text, text_arg};
use crate::string_list::StringList;
use crate::upstream::string_list::ListObject;

/// The keywords that upstream looks for when a module gives none, in the order
/// upstream tries them. Modules call `MangaInfoStatusIfPos` with a single
/// argument and rely on these keywords.
const DEFAULT_KEYWORDS: [&str; 4] = ["ongoing", "complete", "hiatus", "cancel"];

impl SeriesStatus {
    /// Every status, in the order in which upstream tries their keywords.
    const ALL: [Self; 4] = [Self::Ongoing, Self::Completed, Self::Hiatus, Self::Dropped];

    /// Returns the word that `MangaInfoStatusIfPos` gives modules for this
    /// status.
    fn word(self) -> &'static str {
        match self {
            Self::Ongoing => "ongoing",
            Self::Completed => "completed",
            Self::Hiatus => "hiatus",
            Self::Dropped => "dropped",
        }
    }

    /// Reads a status from the text that a module left in `MANGAINFO.Status`.
    ///
    /// That text is usually a word that `MangaInfoStatusIfPos` returned. Some
    /// modules store the site's own text instead, which is read with the
    /// default keywords.
    fn from_text(text: &str) -> Option<Self> {
        let text = text.trim();
        Self::ALL
            .into_iter()
            .find(|status| status.word().eq_ignore_ascii_case(text))
            .or_else(|| Self::from_keywords(text, DEFAULT_KEYWORDS))
    }

    /// Finds the first status, in upstream's order, for which any of the
    /// `|`-separated keywords appears in `text`, ignoring case.
    fn from_keywords(text: &str, keywords: [&str; 4]) -> Option<Self> {
        let text = text.to_lowercase();
        let matches = |list: &str| {
            list.split('|')
                .map(str::trim)
                .any(|keyword| !keyword.is_empty() && text.contains(&keyword.to_lowercase()))
        };
        Self::ALL
            .into_iter()
            .zip(keywords)
            .find(|(_, list)| matches(list))
            .map(|(s, _)| s)
    }
}

/// What `MANGAINFO` holds while `OnGetInfo` runs. The record is kept in the
/// runtime.
#[derive(Debug, Default)]
pub(crate) struct SeriesRecord {
    url: String,
    title: String,
    alt_titles: String,
    /// The cover image's address, which modules write through
    /// `MANGAINFO.CoverLink`.
    cover_url: String,
    authors: String,
    artists: String,
    genres: String,
    status: String,
    summary: String,
    chapter_links: StringList,
    chapter_names: StringList,
}

impl SeriesRecord {
    /// Returns an empty record for the series at `url`.
    pub(crate) fn for_url(url: &str) -> Self {
        Self {
            url: url.to_owned(),
            ..Self::default()
        }
    }

    /// Reads the record into a [`SeriesInfo`].
    ///
    /// Each link is paired with the name at the same position. Links are put
    /// into the shape that the module expects to get back, and an empty link
    /// is dropped, because the module could not read a chapter from it.
    pub(crate) fn into_series(self) -> SeriesInfo {
        let names = self.chapter_names.items();
        let chapters = self
            .chapter_links
            .items()
            .iter()
            .enumerate()
            .map(|(i, link)| Chapter {
                url: normalize_link(link),
                name: names
                    .get(i)
                    .map(|n| n.trim())
                    .filter(|n| !n.is_empty())
                    .map(str::to_owned),
            })
            .filter(|chapter| !chapter.url.is_empty())
            .collect();
        SeriesInfo {
            title: self.title,
            alt_titles: self.alt_titles,
            cover_url: self.cover_url,
            authors: self.authors,
            artists: self.artists,
            genres: self.genres,
            status: SeriesStatus::from_text(&self.status),
            summary: self.summary,
            chapters,
        }
    }
}

/// The `MANGAINFO` global, which reads and writes [`SeriesRecord`].
pub(crate) struct SeriesObject;

impl UserData for SeriesObject {
    /// Exposes the series' details as text fields, and its chapter links and
    /// names as [`ListObject`]s that modules fill in.
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "URL", |m: &mut SeriesRecord| &mut m.url);
        text_field(fields, "Title", |m: &mut SeriesRecord| &mut m.title);
        text_field(fields, "AltTitles", |m: &mut SeriesRecord| {
            &mut m.alt_titles
        });
        text_field(fields, "CoverLink", |m: &mut SeriesRecord| &mut m.cover_url);
        text_field(fields, "Authors", |m: &mut SeriesRecord| &mut m.authors);
        text_field(fields, "Artists", |m: &mut SeriesRecord| &mut m.artists);
        text_field(fields, "Genres", |m: &mut SeriesRecord| &mut m.genres);
        text_field(fields, "Status", |m: &mut SeriesRecord| &mut m.status);
        text_field(fields, "Summary", |m: &mut SeriesRecord| &mut m.summary);
        fields.add_field_function_get("ChapterLinks", |_, _: AnyUserData| {
            Ok(ListObject::<SeriesRecord>::new(|m| &mut m.chapter_links))
        });
        fields.add_field_function_get("ChapterNames", |_, _: AnyUserData| {
            Ok(ListObject::<SeriesRecord>::new(|m| &mut m.chapter_names))
        });
    }

    /// Gives `MANGAINFO` no methods, and makes its unknown fields read as
    /// `nil`.
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        ignore_unknown_fields(methods);
    }
}

/// Sets the globals `MANGAINFO` and `MangaInfoStatusIfPos`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    lua.globals().set("MANGAINFO", SeriesObject)?;
    let status_from_keywords = lua.create_function(status_from_keywords)?;
    lua.globals()
        .set("MangaInfoStatusIfPos", status_from_keywords)
}

/// Implements `MangaInfoStatusIfPos(text, ongoing, completed, hiatus, dropped)`.
/// It returns the word for the first status whose keywords appear in `text`,
/// or `""` if none do.
///
/// Each keyword list is separated by `|`, and a list that is left out or `nil`
/// takes the default. Upstream returns numeric codes instead, but
/// `MANGAINFO.Status` is read back from these words.
fn status_from_keywords(
    lua: &Lua,
    (text, ongoing, completed, hiatus, dropped): (Value, Value, Value, Value, Value),
) -> mlua::Result<&'static str> {
    let text = text_arg(lua, &text, 1)?;
    let lists = [ongoing, completed, hiatus, dropped];
    let mut keywords = DEFAULT_KEYWORDS.map(str::to_owned);
    for (keyword, list) in keywords.iter_mut().zip(&lists) {
        if let Some(list) = lua_text(lua, list) {
            *keyword = list;
        }
    }
    let keywords = [&keywords[0], &keywords[1], &keywords[2], &keywords[3]].map(String::as_str);
    Ok(SeriesStatus::from_keywords(&text, keywords).map_or("", SeriesStatus::word))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_default_keywords_name_each_status() {
        let read = |text: &str| SeriesStatus::from_keywords(text, DEFAULT_KEYWORDS);
        assert_eq!(read("Completed"), Some(SeriesStatus::Completed));
        assert_eq!(read("Ongoing"), Some(SeriesStatus::Ongoing));
        assert_eq!(read("On Hiatus"), Some(SeriesStatus::Hiatus));
        assert_eq!(read("Cancelled"), Some(SeriesStatus::Dropped));
        assert_eq!(read(""), None);
        assert_eq!(read("something else"), None);
        // Ongoing wins when the text matches both lists, because upstream tries
        // ongoing first.
        assert_eq!(read("Ongoing (was completed)"), Some(SeriesStatus::Ongoing));
    }

    #[test]
    fn a_status_reads_back_from_a_word_or_the_sites_own_text() {
        assert_eq!(
            SeriesStatus::from_text("dropped"),
            Some(SeriesStatus::Dropped)
        );
        assert_eq!(
            SeriesStatus::from_text(" Status: Completed "),
            Some(SeriesStatus::Completed)
        );
        assert_eq!(SeriesStatus::from_text("1"), None);
    }

    #[test]
    fn chapters_pair_links_with_names() {
        let mut info = SeriesRecord::for_url("/series/x");
        for link in ["chapter-1", "/chapter-2", "", "https://a.example/3"] {
            info.chapter_links.add(link);
        }
        info.chapter_names.add("One");
        info.chapter_names.add("  ");
        let chapters = info.into_series().chapters;
        let urls: Vec<_> = chapters.iter().map(|c| c.url.as_str()).collect();
        assert_eq!(urls, ["/chapter-1", "/chapter-2", "https://a.example/3"]);
        let names: Vec<_> = chapters.iter().map(Chapter::display_name).collect();
        assert_eq!(names, ["One", "/chapter-2", "https://a.example/3"]);
    }
}
