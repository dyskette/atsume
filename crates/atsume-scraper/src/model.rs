//! The values a scrape returns and a module declares, free of Lua.
//!
//! The objects modules write these through live in `upstream`.

use std::collections::HashMap;
use std::path::PathBuf;

/// One setting a module declares. The settings page is generated from these.
#[derive(Clone, Debug, PartialEq)]
pub struct SiteOption {
    /// The widget to show.
    pub kind: OptionKind,
    /// The key the module reads it back by, through `MODULE.GetOption`.
    pub name: String,
    /// The label to show.
    pub caption: String,
    /// The value before the reader changes it, if the module gave one.
    pub default: Option<OptionValue>,
    /// The choices of a [`OptionKind::ComboBox`], and empty otherwise.
    pub items: Vec<String>,
}

/// A website as a module declared it in `Init()`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct SiteDeclaration {
    /// The upstream identifier.
    pub id: String,
    /// The name shown to readers. Mirrors of one site share it.
    pub name: String,
    /// The address the module builds its requests from.
    pub root_url: String,
    /// The language or kind of site, as upstream groups them.
    pub category: String,
    /// Maps each event the module handles to the Lua function handling it.
    pub handlers: HashMap<Event, String>,
    /// The settings the site offers.
    pub options: Vec<SiteOption>,
    /// Whether the site takes a login.
    pub account_support: bool,
    /// Whether the site lists its directory in a stable order.
    pub sorted_list: bool,
    /// How many separate listings the directory is split into; at least one.
    pub directories: u32,
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

/// An event a module may handle, named after the slot it assigns in `Init()`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Event {
    /// Lists one page of the site's directory.
    GetNameAndLink,
    /// Reads a series' details and chapter list.
    GetInfo,
    /// Lists a chapter's pages.
    GetPageNumber,
    /// Counts the pages of the site's directory.
    GetDirectoryPageNumber,
    /// Resolves a page to its image address.
    GetImageUrl,
    /// Prepares the request for a page image, usually by setting a `Referer`.
    BeforeDownloadImage,
    /// Fetches a page image itself, possibly transforming it.
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
    /// Prepares a directory update.
    BeforeUpdateList,
    /// Finishes a directory update.
    AfterUpdateList,
}

impl Event {
    /// Every event, in upstream's order.
    pub const ALL: [Self; 15] = [
        Self::GetNameAndLink,
        Self::GetInfo,
        Self::GetPageNumber,
        Self::GetDirectoryPageNumber,
        Self::GetImageUrl,
        Self::BeforeDownloadImage,
        Self::DownloadImage,
        Self::AfterImageSaved,
        Self::SaveImage,
        Self::Login,
        Self::TaskStart,
        Self::CheckSite,
        Self::AccountState,
        Self::BeforeUpdateList,
        Self::AfterUpdateList,
    ];

    /// Returns the declaration field a module assigns the handler to.
    #[must_use]
    pub fn slot(self) -> &'static str {
        match self {
            Self::GetNameAndLink => "OnGetNameAndLink",
            Self::GetInfo => "OnGetInfo",
            Self::GetPageNumber => "OnGetPageNumber",
            Self::GetDirectoryPageNumber => "OnGetDirectoryPageNumber",
            Self::GetImageUrl => "OnGetImageURL",
            Self::BeforeDownloadImage => "OnBeforeDownloadImage",
            Self::DownloadImage => "OnDownloadImage",
            Self::AfterImageSaved => "OnAfterImageSaved",
            Self::SaveImage => "OnSaveImage",
            Self::Login => "OnLogin",
            Self::TaskStart => "OnTaskStart",
            Self::CheckSite => "OnCheckSite",
            Self::AccountState => "OnAccountState",
            Self::BeforeUpdateList => "OnBeforeUpdateList",
            Self::AfterUpdateList => "OnAfterUpdateList",
        }
    }
}

impl std::fmt::Display for Event {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.slot())
    }
}

/// The widget a module asks for when declaring a setting.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OptionKind {
    /// A boolean switch.
    CheckBox,
    /// A number.
    SpinEdit,
    /// A choice from a list; the value is the chosen item's index.
    ComboBox,
    /// Free text.
    EditBox,
}

/// A setting's value, kept free of Lua types so callers never depend on them.
#[derive(Clone, Debug, PartialEq)]
pub enum OptionValue {
    /// A checkbox state.
    Bool(bool),
    /// A whole number. A whole float such as `1.0` is read as this.
    Int(i64),
    /// A fractional number.
    Float(f64),
    /// Text.
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

/// One page of a site's directory.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct DirectoryPage {
    /// The series on the page, in the order the module listed them.
    pub entries: Vec<DirectoryEntry>,
    /// How many pages the directory has, if the module said while reading
    /// this one. Some modules say so here rather than in
    /// [`Session::directory_page_count`](crate::Session::directory_page_count).
    pub reported_page_count: Option<u32>,
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

/// One page of a chapter.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ChapterPage {
    /// The page's image address.
    Image(String),
    /// A page whose address the module finds only when asked, with
    /// [`Session::resolve_page`](crate::Session::resolve_page).
    Unresolved,
}
