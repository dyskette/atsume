//! One scrape of one website.

use std::collections::HashMap;
use std::path::Path;

use mlua::{AppDataRefMut, Value};

use crate::error::{Error, ErrorKind, Result};
use crate::http::{HttpConfig, HttpSession, LastRequest};
use crate::model::{
    AccountStatus, ChapterPage, DirectoryPage, Event, OptionKind, OptionValue, SeriesInfo,
    SiteDeclaration,
};
use crate::runtime::Runtime;
use crate::runtime::bind::state;
use crate::runtime::budget::DEFAULT_CPU_LIMIT;
use crate::runtime::convert::{to_int, truthy};
use crate::runtime::sandbox::WritableFile;
use crate::upstream::chapter_pages::ChapterPagesRecord;
use crate::upstream::directory::{DirectoryPageRecord, StatusSink};
use crate::upstream::globals;
use crate::upstream::module::{AccountRecord, ModuleObject, SiteRecord};
use crate::upstream::series::SeriesRecord;

/// How the sessions a [`Site`](crate::Site) opens reach the network and how
/// long their calls may run.
#[derive(Clone)]
pub struct SessionConfig {
    /// How requests reach the network.
    pub http: HttpConfig,
    /// How many Lua instructions one call into the module may run before it
    /// is stopped, so a runaway loop cannot hold a worker. Time spent waiting
    /// on the network does not count.
    pub cpu_limit: u64,
}

impl Default for SessionConfig {
    fn default() -> Self {
        Self {
            http: HttpConfig::default(),
            cpu_limit: DEFAULT_CPU_LIMIT,
        }
    }
}

impl SessionConfig {
    /// Returns a configuration that refuses every request, for loading the
    /// catalog: declaring a website needs no network access.
    pub(crate) fn offline() -> Self {
        Self {
            http: HttpConfig::offline(),
            cpu_limit: DEFAULT_CPU_LIMIT,
        }
    }
}

/// A website's module loaded into its own Lua runtime, for one scrape.
///
/// Open one with [`Site::open`](crate::Site::open). A session keeps its
/// cookies and settings across calls, as modules expect. It is not `Send`:
/// open it on the thread that runs the scrape and keep it there, a thread
/// that may block, since requests wait on the network.
pub struct Session {
    runtime: Runtime,
}

impl Session {
    pub(crate) fn new(runtime: Runtime, declaration: SiteDeclaration) -> Result<Self> {
        // The one value `upstream::install` leaves out: it needs the declaration.
        runtime.lua.set_app_data(SiteRecord::new(declaration));
        runtime.lua.globals().set("MODULE", ModuleObject)?;
        Ok(Self { runtime })
    }

    /// Borrows state the runtime keeps, between calls into the module.
    ///
    /// Every state value is set when the session is built, and only a binding
    /// running inside a call holds one, so no borrow can be outstanding here.
    #[expect(
        clippy::expect_used,
        reason = "the session sets every state value before handing itself out, and calls into it never overlap a binding"
    )]
    fn state<S: 'static>(&self) -> AppDataRefMut<'_, S> {
        state::<S>(&self.runtime.lua).expect("session state is set and not in use")
    }

    /// Returns the website as its declaration stands now. A module may change
    /// some of it while it runs, such as its address.
    #[must_use]
    pub fn declaration(&self) -> SiteDeclaration {
        self.state::<SiteRecord>().declaration.clone()
    }

    /// Returns the website's name, for error messages.
    fn site_name(&self) -> String {
        self.state::<SiteRecord>().declaration.name.clone()
    }

    /// Returns the address the website is reached at now.
    fn root_url(&self) -> String {
        self.state::<SiteRecord>().declaration.root_url.clone()
    }

    /// Reports whether the module implements `event`.
    #[must_use]
    pub fn has_handler(&self, event: Event) -> bool {
        self.state::<SiteRecord>()
            .declaration
            .handler(event)
            .is_some()
    }

    /// Changes a setting the module declares.
    ///
    /// # Errors
    ///
    /// Fails if the module declares no such setting, or the value does not
    /// suit it: a checkbox takes a flag, a number field a whole number, a
    /// choice the index of one of its items, and a text field text.
    pub fn set_option(&mut self, name: &str, value: OptionValue) -> Result<()> {
        let mut site = self.state::<SiteRecord>();
        let declaration = &site.declaration;
        let Some(option) = declaration.options.iter().find(|o| o.name == name) else {
            let message = format!("module {} has no setting named {name:?}", declaration.name);
            return Err(Error::new(ErrorKind::Unsupported, message));
        };
        let suits = match (&value, option.kind) {
            (OptionValue::Bool(_), OptionKind::CheckBox)
            | (OptionValue::Int(_), OptionKind::SpinEdit)
            | (OptionValue::Text(_), OptionKind::EditBox) => true,
            (OptionValue::Int(index), OptionKind::ComboBox) => {
                usize::try_from(*index).is_ok_and(|i| i < option.items.len())
            }
            _ => false,
        };
        if !suits {
            let message = format!("setting {name:?} does not take {value:?}");
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        site.options.insert(name.to_owned(), value);
        Ok(())
    }

    /// Supplies credentials for a module that implements `OnLogin`.
    pub fn set_account(&mut self, username: &str, password: &str) {
        let mut account = self.state::<AccountRecord>();
        account.enabled = !username.is_empty();
        username.clone_into(&mut account.username);
        password.clone_into(&mut account.password);
    }

    /// Returns the login state the module recorded.
    #[must_use]
    pub fn account_status(&self) -> AccountStatus {
        self.state::<AccountRecord>().status
    }

    /// Points `MODULE.CurrentDirectoryIndex` at directory listing `index`,
    /// counting from 0, which the module reads to know which listing to walk.
    fn select_listing(&mut self, index: u32) -> Result<()> {
        let mut site = self.state::<SiteRecord>();
        if index >= site.declaration.listing_count {
            let message = format!(
                "module {} has {} directory listings, not {}",
                site.declaration.name,
                site.declaration.listing_count,
                index + 1
            );
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        site.listing_index = i64::from(index);
        Ok(())
    }

    /// Returns what the last request the module made got back.
    #[must_use]
    pub fn last_request(&self) -> LastRequest {
        self.state::<HttpSession>().last.clone()
    }

    /// Runs `OnGetNameAndLink` for one page of directory listing `listing`,
    /// both counting from 0.
    ///
    /// # Errors
    ///
    /// Fails if the site has no such listing
    /// ([`Unsupported`](ErrorKind::Unsupported)), the module has no
    /// `OnGetNameAndLink` handler, the handler fails, or a request it needed
    /// failed ([`Network`](ErrorKind::Network)).
    pub fn list(&mut self, listing: u32, page: u32) -> Result<DirectoryPage> {
        self.select_listing(listing)?;
        *self.state::<DirectoryPageRecord>() = DirectoryPageRecord::default();
        globals::set_url_to_directory_page(&self.runtime.lua, page)?;
        let code = to_int(&self.runtime.lua, &self.call(Event::GetNameAndLink)?);
        if code == globals::NET_PROBLEM {
            let what = format!("listing page {}", u64::from(page) + 1);
            return Err(Error::network(
                &self.site_name(),
                &what,
                self.last_request(),
            ));
        }
        Ok(std::mem::take(&mut *self.state::<DirectoryPageRecord>()).into_page())
    }

    /// Sends the progress messages a module reports while it runs, through
    /// `UPDATELIST.UpdateStatusText`, to `report`.
    pub fn on_status(&mut self, report: impl FnMut(&str) + 'static) {
        self.state::<StatusSink>().0 = Some(Box::new(report));
    }

    /// Runs `OnGetDirectoryPageNumber`, returning how many pages directory
    /// listing `listing`, counting from 0, has. A module without the
    /// handler has one.
    ///
    /// # Errors
    ///
    /// Fails if the site has no such listing
    /// ([`Unsupported`](ErrorKind::Unsupported)) or the handler fails.
    pub fn directory_page_count(&mut self, listing: u32) -> Result<u32> {
        self.select_listing(listing)?;
        if !self.has_handler(Event::GetDirectoryPageNumber) {
            return Ok(1);
        }
        globals::set_directory_page_count(&self.runtime.lua, 1)?;
        self.call(Event::GetDirectoryPageNumber)?;
        let pages = globals::directory_page_count(&self.runtime.lua)?;
        Ok(u32::try_from(pages.max(1)).unwrap_or(u32::MAX))
    }

    /// Runs `OnGetInfo` for the series at `series_url`, returning its details
    /// and chapters.
    ///
    /// # Errors
    ///
    /// Fails if the module has no `OnGetInfo` handler, the handler fails, a
    /// request it needed failed ([`Network`](ErrorKind::Network)), or the page
    /// holds no series ([`NotFound`](ErrorKind::NotFound)).
    pub fn info(&mut self, series_url: &str) -> Result<SeriesInfo> {
        *self.state::<SeriesRecord>() = SeriesRecord::for_url(series_url);
        globals::set_url(&self.runtime.lua, series_url)?;
        let code = to_int(&self.runtime.lua, &self.call(Event::GetInfo)?);
        let site_name = self.site_name();
        match code {
            globals::NET_PROBLEM => {
                let what = format!("fetching {series_url}");
                return Err(Error::network(&site_name, &what, self.last_request()));
            }
            globals::INFORMATION_NOT_FOUND => {
                let message = format!(
                    "{site_name}: no series information at {series_url}; the page may have moved"
                );
                return Err(Error::new(ErrorKind::NotFound, message));
            }
            _ => {}
        }
        Ok(std::mem::take(&mut *self.state::<SeriesRecord>()).into_series())
    }

    /// Runs `OnGetPageNumber` for the chapter at `chapter_url`, returning its
    /// pages in reading order.
    ///
    /// Image addresses are made absolute against the site's address. A page
    /// the module finds only when asked is [`ChapterPage::Unresolved`]; resolve it
    /// with [`resolve_page`](Self::resolve_page).
    ///
    /// # Errors
    ///
    /// Fails if the module has no `OnGetPageNumber` handler, the handler fails
    /// or declines, a request it needed failed
    /// ([`Network`](ErrorKind::Network)), or it lists no pages
    /// ([`NotFound`](ErrorKind::NotFound)).
    pub fn pages(&mut self, chapter_url: &str) -> Result<Vec<ChapterPage>> {
        *self.state::<ChapterPagesRecord>() = ChapterPagesRecord::for_chapter(chapter_url);
        globals::set_url(&self.runtime.lua, chapter_url)?;
        let result = self.call(Event::GetPageNumber)?;
        self.check_outcome(&result, &format!("reading pages for {chapter_url}"))?;

        let resolvable = self.has_handler(Event::GetImageUrl);
        let pages = self
            .state::<ChapterPagesRecord>()
            .pages(&self.root_url(), resolvable);
        if pages.is_empty() {
            let message = format!("{}: no pages listed for {chapter_url}", self.site_name());
            return Err(Error::new(ErrorKind::NotFound, message));
        }
        Ok(pages)
    }

    /// Runs `OnGetImageURL` for the page at `index`, counting from 0, of the
    /// chapter whose pages were last listed, and returns its image address.
    ///
    /// A page the module already resolved, as some do for every page on the
    /// first call, is returned without running the handler again.
    ///
    /// # Errors
    ///
    /// Fails if `chapter_url` is not the chapter last listed with
    /// [`pages`](Self::pages), the module has no `OnGetImageURL` handler, the
    /// handler fails or declines, a request it needed failed
    /// ([`Network`](ErrorKind::Network)), or it leaves the page without an
    /// address.
    pub fn resolve_page(&mut self, chapter_url: &str, index: usize) -> Result<String> {
        let root_url = self.root_url();
        if !self.state::<ChapterPagesRecord>().is_for(chapter_url) {
            let message = format!("list the pages of {chapter_url} before resolving one");
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        if let ChapterPage::Image(url) = self.state::<ChapterPagesRecord>().page(&root_url, index) {
            return Ok(url);
        }
        globals::set_url(&self.runtime.lua, chapter_url)?;
        globals::set_page_to_resolve(&self.runtime.lua, index)?;
        let what = format!("resolving page {} of {chapter_url}", index + 1);
        let result = self.call(Event::GetImageUrl)?;
        self.check_outcome(&result, &what)?;
        match self.state::<ChapterPagesRecord>().page(&root_url, index) {
            ChapterPage::Image(url) => Ok(url),
            ChapterPage::Unresolved => {
                let message = format!("{}: no image address after {what}", self.site_name());
                Err(Error::new(ErrorKind::Module, message))
            }
        }
    }

    /// Turns a page handler's result into an error if it reports one.
    ///
    /// Page handlers signal success either way upstream allows: `true` or
    /// `no_error`. `false` or `nil` is a refusal, and `net_problem` is a
    /// failed request, though Lua reads both codes as true.
    fn check_outcome(&self, result: &Value, what: &str) -> Result<()> {
        let site_name = self.site_name();
        let net_problem = globals::NET_PROBLEM;
        match result {
            Value::Nil | Value::Boolean(false) => {
                let status = self.last_request().status;
                let message = format!("{site_name}: declined {what} (last status {status})");
                Err(Error::new(ErrorKind::Module, message))
            }
            Value::Integer(code) if *code == net_problem => {
                Err(Error::network(&site_name, what, self.last_request()))
            }
            _ => Ok(()),
        }
    }

    /// Runs `OnLogin`, reporting whether the site accepted the credentials.
    ///
    /// # Errors
    ///
    /// Fails if the module has no login handler, no credentials were set, the
    /// handler fails, or the module recorded the credentials as invalid even
    /// though the handler succeeded.
    pub fn login(&mut self) -> Result<bool> {
        let site_name = self.site_name();
        if !self.has_handler(Event::Login) {
            return Err(no_handler(&site_name, Event::Login));
        }
        if !self.state::<AccountRecord>().enabled {
            let message = format!("module {site_name} has no credentials configured");
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        let accepted = truthy(&self.call(Event::Login)?);
        if self.account_status() == AccountStatus::Invalid {
            let message = format!("module {site_name} rejected the credentials");
            return Err(Error::new(ErrorKind::LoginRejected, message));
        }
        Ok(accepted)
    }

    /// Runs `OnBeforeDownloadImage` and returns the request headers the module
    /// set, usually a `Referer` the image host demands.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails.
    pub fn before_download_image(&mut self, image_url: &str) -> Result<HashMap<String, String>> {
        if !self.has_handler(Event::BeforeDownloadImage) {
            return Ok(HashMap::new());
        }
        self.state::<HttpSession>().headers.clear();
        globals::set_url(&self.runtime.lua, image_url)?;
        self.call(Event::BeforeDownloadImage)?;
        let http = self.state::<HttpSession>();
        Ok(http
            .headers
            .pairs()
            .map(|(n, v)| (n.to_owned(), v.to_owned()))
            .collect())
    }

    /// Runs `OnDownloadImage`, in which the module fetches the image itself and
    /// may transform it, and returns the bytes it left in `HTTP.Document`.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails, returns false, or leaves no data.
    pub fn download_image(&mut self, image_url: &str) -> Result<Vec<u8>> {
        self.state::<HttpSession>().document.clear();
        globals::set_url(&self.runtime.lua, image_url)?;
        let site_name = self.site_name();
        if !truthy(&self.call(Event::DownloadImage)?) {
            let message = format!("{site_name}: module declined to download {image_url}");
            return Err(Error::new(ErrorKind::Module, message));
        }
        let data = std::mem::take(&mut self.state::<HttpSession>().document);
        if data.is_empty() {
            let message = format!("{site_name}: module returned no image data for {image_url}");
            return Err(Error::new(ErrorKind::Module, message));
        }
        Ok(data)
    }

    /// Runs `OnAfterImageSaved` on a downloaded page, which the module may
    /// rewrite in place, for instance to remove a watermark.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails.
    pub fn after_image_saved(&mut self, path: &Path) -> Result<()> {
        if !self.has_handler(Event::AfterImageSaved) {
            return Ok(());
        }
        globals::set_filename(&self.runtime.lua, path)?;
        self.state::<WritableFile>().0 = Some(path.to_path_buf());
        let result = self.call(Event::AfterImageSaved);
        self.state::<WritableFile>().0 = None;
        result.map(|_| ())
    }

    /// Calls the Lua function bound to `event` and returns its result.
    pub(crate) fn call(&self, event: Event) -> Result<Value> {
        let site_name = self.site_name();
        let function = self
            .state::<SiteRecord>()
            .declaration
            .handler(event)
            .map(str::to_owned);
        let Some(function) = function else {
            return Err(no_handler(&site_name, event));
        };
        let Value::Function(handler) = self.runtime.lua.globals().get(function.as_str())? else {
            let message = format!(
                "module {site_name} declares {event}={function:?} but defines no such function"
            );
            return Err(Error::new(ErrorKind::Module, message));
        };
        let context = format!("{site_name}/{function}");
        self.runtime
            .protect(|_| handler.call::<Value>(()))
            .map_err(|e| e.within(context, ErrorKind::Module))
    }

    /// Returns the most instructions any one call has used so far, for
    /// calibrating the CPU limit.
    #[cfg(test)]
    pub(crate) fn peak_instructions(&self) -> u64 {
        self.runtime.budget.peak()
    }

    /// Points `event` at another Lua function, for tests.
    #[cfg(test)]
    pub(crate) fn set_handler(&self, event: Event, function: &str) {
        let mut site = self.state::<SiteRecord>();
        site.declaration.handlers.insert(event, function.to_owned());
    }

    /// Runs Lua statements in the session's globals, for tests.
    #[cfg(test)]
    pub(crate) fn exec(&self, statements: &str) {
        self.runtime.lua.load(statements).exec().unwrap();
    }

    /// Evaluates a Lua expression in the session's globals and returns it as
    /// text, for tests to look inside a session.
    #[cfg(test)]
    pub(crate) fn eval(&self, expression: &str) -> String {
        let lua = &self.runtime.lua;
        let value: Value = lua.load(format!("return {expression}")).eval().unwrap();
        let text = lua.coerce_string(value).unwrap();
        text.map(|s| s.to_string_lossy()).unwrap_or_default()
    }

    /// Returns the session's Lua runtime, for tests to define helpers in.
    #[cfg(test)]
    pub(crate) fn lua(&self) -> &mlua::Lua {
        &self.runtime.lua
    }
}

/// The error for an event the module does not handle.
fn no_handler(site_name: &str, event: Event) -> Error {
    Error::new(
        ErrorKind::Unsupported,
        format!("module {site_name} has no {event} handler"),
    )
}

#[cfg(test)]
mod tests;
