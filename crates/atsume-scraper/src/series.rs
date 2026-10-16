//! A series' details, which `OnGetInfo` writes into `MANGAINFO`.

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::bind::{lenient, text_field};
use crate::convert::{string_arg, text_of};
use crate::links::normalise_link;
use crate::strings::{ListHandle, Strings};

/// A series as a module describes it.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct SeriesInfo {
    /// The title.
    pub title: String,
    /// Other titles, as the site lists them.
    pub alt_titles: String,
    /// The cover image's address.
    pub cover_url: String,
    /// The authors, as the site lists them.
    pub authors: String,
    /// The artists, as the site lists them.
    pub artists: String,
    /// The genres, as the site lists them.
    pub genres: String,
    /// Whether the series is still running, if the site says.
    pub status: Option<SeriesStatus>,
    /// The description.
    pub summary: String,
    /// The chapters, in the order the module listed them.
    pub chapters: Vec<Chapter>,
}

/// One chapter of a series.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Chapter {
    /// The link the module reads the chapter's pages from.
    pub url: String,
    /// The name, unless the module gave none.
    pub name: Option<String>,
}

impl Chapter {
    /// Returns the name to show: the chapter's name, or its link if it has
    /// none.
    #[must_use]
    pub fn display_name(&self) -> &str {
        self.name.as_deref().unwrap_or(&self.url)
    }
}

/// Whether a series is still running.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum SeriesStatus {
    /// New chapters are still coming.
    Ongoing,
    /// The series has ended.
    Completed,
    /// The series is paused.
    Hiatus,
    /// The series was cancelled.
    Dropped,
}

/// The keywords upstream looks for when a module gives none, in the order it
/// tries them. Modules call `MangaInfoStatusIfPos` with one argument and rely
/// on these.
const DEFAULT_KEYWORDS: [&str; 4] = ["ongoing", "complete", "hiatus", "cancel"];

impl SeriesStatus {
    /// Every status, in the order upstream tries their keywords.
    const ALL: [Self; 4] = [Self::Ongoing, Self::Completed, Self::Hiatus, Self::Dropped];

    /// Returns the word `MangaInfoStatusIfPos` gives modules for this status.
    fn word(self) -> &'static str {
        match self {
            Self::Ongoing => "ongoing",
            Self::Completed => "completed",
            Self::Hiatus => "hiatus",
            Self::Dropped => "dropped",
        }
    }

    /// Reads a status from the text a module left in `MANGAINFO.Status`.
    ///
    /// That is usually a word `MangaInfoStatusIfPos` returned, but some
    /// modules store the site's own text, which is read with the default
    /// keywords.
    fn from_text(text: &str) -> Option<Self> {
        let text = text.trim();
        Self::ALL
            .into_iter()
            .find(|status| status.word().eq_ignore_ascii_case(text))
            .or_else(|| Self::from_keywords(text, DEFAULT_KEYWORDS))
    }

    /// Finds the first status, in upstream's order, any of whose
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

/// What `MANGAINFO` holds while `OnGetInfo` runs, kept in the runtime.
#[derive(Debug, Default)]
pub(crate) struct MangaInfo {
    url: String,
    title: String,
    alt_titles: String,
    cover_link: String,
    authors: String,
    artists: String,
    genres: String,
    status: String,
    summary: String,
    chapter_links: Strings,
    chapter_names: Strings,
}

impl MangaInfo {
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
    /// in the shape the module expects back, and an empty link is dropped,
    /// since the module could not read a chapter from it.
    pub(crate) fn into_series(self) -> SeriesInfo {
        let names = self.chapter_names.items();
        let chapters = self
            .chapter_links
            .items()
            .iter()
            .enumerate()
            .map(|(i, link)| Chapter {
                url: normalise_link(link),
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
            cover_url: self.cover_link,
            authors: self.authors,
            artists: self.artists,
            genres: self.genres,
            status: SeriesStatus::from_text(&self.status),
            summary: self.summary,
            chapters,
        }
    }
}

/// `MANGAINFO`. It holds nothing itself; every access reaches [`MangaInfo`].
pub(crate) struct MangaInfoObject;

impl UserData for MangaInfoObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "URL", |m: &mut MangaInfo| &mut m.url);
        text_field(fields, "Title", |m: &mut MangaInfo| &mut m.title);
        text_field(fields, "AltTitles", |m: &mut MangaInfo| &mut m.alt_titles);
        text_field(fields, "CoverLink", |m: &mut MangaInfo| &mut m.cover_link);
        text_field(fields, "Authors", |m: &mut MangaInfo| &mut m.authors);
        text_field(fields, "Artists", |m: &mut MangaInfo| &mut m.artists);
        text_field(fields, "Genres", |m: &mut MangaInfo| &mut m.genres);
        text_field(fields, "Status", |m: &mut MangaInfo| &mut m.status);
        text_field(fields, "Summary", |m: &mut MangaInfo| &mut m.summary);
        fields.add_field_function_get("ChapterLinks", |_, _: AnyUserData| {
            Ok(ListHandle::<MangaInfo>::new(|m| &mut m.chapter_links))
        });
        fields.add_field_function_get("ChapterNames", |_, _: AnyUserData| {
            Ok(ListHandle::<MangaInfo>::new(|m| &mut m.chapter_names))
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        lenient(methods);
    }
}

/// Sets up `MANGAINFO` and `MangaInfoStatusIfPos` in a runtime.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    lua.set_app_data(MangaInfo::default());
    lua.globals().set("MANGAINFO", MangaInfoObject)?;
    let status_if_pos = lua.create_function(status_if_pos)?;
    lua.globals().set("MangaInfoStatusIfPos", status_if_pos)
}

/// `MangaInfoStatusIfPos(text, ongoing, completed, hiatus, dropped)`: the word
/// for the first status whose keywords appear in `text`, or `""`.
///
/// Each keyword list is `|`-separated, and a list left out or `nil` takes the
/// default. Upstream returns numeric codes; these words are what
/// `MANGAINFO.Status` is read back from.
fn status_if_pos(
    lua: &Lua,
    (text, ongoing, completed, hiatus, dropped): (Value, Value, Value, Value, Value),
) -> mlua::Result<&'static str> {
    let text = string_arg(lua, &text, 1)?;
    let lists = [ongoing, completed, hiatus, dropped];
    let mut keywords = DEFAULT_KEYWORDS.map(str::to_owned);
    for (keyword, list) in keywords.iter_mut().zip(&lists) {
        if let Some(list) = text_of(lua, list) {
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
        // Ongoing wins when a status matches both lists, as upstream orders it.
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
        let mut info = MangaInfo::for_url("/series/x");
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
