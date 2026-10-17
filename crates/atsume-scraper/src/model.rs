//! The values that a scrape returns and that a module declares. None of them
//! holds a Lua type.
//!
//! The Lua objects through which modules fill in these values live in
//! `upstream`.

use std::collections::HashMap;
use std::path::PathBuf;

/// One setting that a module declares. The settings page is generated from
/// these declarations.
#[derive(Clone, Debug, PartialEq)]
pub struct SiteOption {
    /// The kind of value the setting holds, which decides the widget to show.
    pub kind: OptionKind,
    /// The key by which the module reads the setting's value, through
    /// `MODULE.GetOption`.
    pub name: String,
    /// The label to show next to the setting.
    pub caption: String,
    /// The value the setting has until the reader changes it, if the module
    /// gave one.
    pub default: Option<OptionValue>,
    /// The choices of an [`OptionKind::Choice`] setting. Settings of other
    /// kinds have none.
    pub choices: Vec<String>,
}

/// A website as a module declared it in `Init()`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SiteDeclaration {
    /// The website's identifier in FMD2.
    pub id: String,
    /// The name shown to readers. Mirrors of one site share it.
    pub name: String,
    /// The address the module builds its requests from.
    pub root_url: String,
    /// The language or kind of site, as FMD2 groups websites.
    pub category: String,
    /// Maps each event the module handles to the name of the Lua function that
    /// handles it.
    pub handlers: HashMap<Event, String>,
    /// The settings the site offers.
    pub options: Vec<SiteOption>,
    /// Whether the site takes a login, as the declaration's `AccountSupport`
    /// field says.
    pub takes_login: bool,
    /// Whether the site lists its directory in a stable order, as the
    /// declaration's `SortedList` field says.
    pub lists_in_stable_order: bool,
    /// How many separate listings the directory is split into, which is at
    /// least one. The declaration gives it in its `TotalDirectory` field.
    pub listing_count: u32,
    /// The file the module was loaded from.
    pub file: PathBuf,
}

impl SiteDeclaration {
    /// Returns the Lua function name handling `event`, if the module set one.
    #[must_use]
    pub fn handler(&self, event: Event) -> Option<&str> {
        self.handlers.get(&event).map(String::as_str)
    }
}

/// An event that a module can handle.
///
/// Each variant is named after the job the module does for that event. A
/// module assigns its handler to a field of its declaration in `Init()`, and
/// [`declaration_field`](Self::declaration_field) returns that field's name.
/// When the field's name differs from the variant's, the variant's
/// documentation gives it in parentheses.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Event {
    /// Lists one page of the site's directory (`OnGetNameAndLink`).
    ListDirectoryPage,
    /// Reads a series' details and chapter list (`OnGetInfo`).
    ReadSeries,
    /// Lists a chapter's pages (`OnGetPageNumber`).
    ListChapterPages,
    /// Counts the pages of the site's directory (`OnGetDirectoryPageNumber`).
    CountDirectoryPages,
    /// Finds the image address of one page of a chapter (`OnGetImageURL`).
    ResolvePageImage,
    /// Prepares the request for a page image, usually by setting a `Referer`
    /// header.
    BeforeDownloadImage,
    /// Fetches a page image itself, and may transform it.
    DownloadImage,
    /// Edits a saved page image in place.
    AfterImageSaved,
    /// Saves a page image itself.
    SaveImage,
    /// Logs in with the configured account.
    Login,
    /// Prepares a download task.
    TaskStart,
    /// Checks that the site is reachable.
    CheckSite,
    /// Reports the account's state.
    AccountState,
    /// Prepares a directory update (`OnBeforeUpdateList`).
    BeforeDirectoryUpdate,
    /// Finishes a directory update (`OnAfterUpdateList`).
    AfterDirectoryUpdate,
}

impl Event {
    /// Every event, in upstream's order.
    pub const ALL: [Self; 15] = [
        Self::ListDirectoryPage,
        Self::ReadSeries,
        Self::ListChapterPages,
        Self::CountDirectoryPages,
        Self::ResolvePageImage,
        Self::BeforeDownloadImage,
        Self::DownloadImage,
        Self::AfterImageSaved,
        Self::SaveImage,
        Self::Login,
        Self::TaskStart,
        Self::CheckSite,
        Self::AccountState,
        Self::BeforeDirectoryUpdate,
        Self::AfterDirectoryUpdate,
    ];

    /// Returns the name of the declaration field to which a module assigns its
    /// handler for this event.
    #[must_use]
    pub fn declaration_field(self) -> &'static str {
        match self {
            Self::ListDirectoryPage => "OnGetNameAndLink",
            Self::ReadSeries => "OnGetInfo",
            Self::ListChapterPages => "OnGetPageNumber",
            Self::CountDirectoryPages => "OnGetDirectoryPageNumber",
            Self::ResolvePageImage => "OnGetImageURL",
            Self::BeforeDownloadImage => "OnBeforeDownloadImage",
            Self::DownloadImage => "OnDownloadImage",
            Self::AfterImageSaved => "OnAfterImageSaved",
            Self::SaveImage => "OnSaveImage",
            Self::Login => "OnLogin",
            Self::TaskStart => "OnTaskStart",
            Self::CheckSite => "OnCheckSite",
            Self::AccountState => "OnAccountState",
            Self::BeforeDirectoryUpdate => "OnBeforeUpdateList",
            Self::AfterDirectoryUpdate => "OnAfterUpdateList",
        }
    }
}

impl std::fmt::Display for Event {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.declaration_field())
    }
}

/// The kind of value that a setting holds.
///
/// Each kind has its own widget, and modules declare each kind with its own
/// setter. The variant's documentation names that setter in parentheses.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OptionKind {
    /// A boolean switch (`AddOptionCheckBox`).
    Toggle,
    /// A number (`AddOptionSpinEdit`).
    Number,
    /// A choice from a list (`AddOptionComboBox`). The setting's value is the
    /// index of the chosen item.
    Choice,
    /// Free text (`AddOptionEdit` or `AddOptionEditBox`).
    Text,
}

/// A setting's value. It holds no Lua types, so callers never depend on them.
#[derive(Clone, Debug, PartialEq)]
pub enum OptionValue {
    /// A toggle's state.
    Bool(bool),
    /// A whole number. A float with no fractional part, such as `1.0`, is read
    /// as this variant.
    Int(i64),
    /// A fractional number.
    Float(f64),
    /// A text value.
    Text(String),
}

/// A login state, as a module records it in `MODULE.Account.Status`.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum AccountStatus {
    /// Not checked yet.
    #[default]
    Unknown,
    /// A login is in progress.
    Checking,
    /// The site accepted the credentials.
    Valid,
    /// The site rejected the credentials.
    Invalid,
}

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
    /// Whether the series is still running, if the site says so.
    pub status: Option<SeriesStatus>,
    /// The series' description.
    pub summary: String,
    /// The chapters, in the order the module listed them.
    pub chapters: Vec<Chapter>,
}

/// One chapter of a series.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Chapter {
    /// The link the module reads the chapter's pages from.
    pub url: String,
    /// The chapter's name, unless the module gave none.
    pub name: Option<String>,
}

impl Chapter {
    /// Returns the name to show, which is the chapter's name or, if it has
    /// none, its link.
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

/// One page of a site's directory.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DirectoryPage {
    /// The series on the page, in the order the module listed them.
    pub entries: Vec<DirectoryEntry>,
    /// How many pages the directory has, if the module reported it while
    /// reading this page. Some modules report it here instead of in
    /// [`Session::directory_page_count`](crate::Session::directory_page_count).
    pub reported_page_count: Option<u32>,
}

/// One series in a site's directory.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct DirectoryEntry {
    /// The link to read the series from with
    /// [`Session::series_info`](crate::Session::series_info).
    pub url: String,
    /// The series' title, unless the module gave none.
    pub name: Option<String>,
}

impl DirectoryEntry {
    /// Returns the name to show, which is the entry's title or, if it has
    /// none, its link.
    #[must_use]
    pub fn display_name(&self) -> &str {
        self.name.as_deref().unwrap_or(&self.url)
    }
}

/// One page of a chapter.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ChapterPage {
    /// The page's image address.
    ImageUrl(String),
    /// A page whose image address the module finds only when asked.
    /// [`Session::resolve_page`](crate::Session::resolve_page) asks for it.
    Unresolved,
}
