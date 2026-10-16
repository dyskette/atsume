//! One scrape of one website.

use std::collections::HashMap;
use std::path::Path;

use mlua::{AppDataRefMut, Value};

use crate::bind::state;
use crate::budget::DEFAULT_CPU_LIMIT;
use crate::convert::{to_int, truthy};
use crate::error::{Error, ErrorKind, Result};
use crate::globals;
use crate::http::{HttpConfig, HttpSession, LastRequest};
use crate::module::{Event, Module, OptionKind, OptionValue};
use crate::runtime::Runtime;
use crate::sandbox::WritableFile;
use crate::series::{MangaInfo, SeriesInfo};
use crate::site::{Account, AccountStatus, ModuleObject, SiteState};
use crate::task::{Page, Task};

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
/// Open one with [`Site::open`](crate::Site::open). A session is
/// single-threaded and keeps its cookies and settings across calls, as
/// modules expect. Run it on a thread that may block: requests wait on the
/// network.
pub struct Session {
    runtime: Runtime,
}

impl Session {
    pub(crate) fn new(runtime: Runtime, module: Module) -> Result<Self> {
        runtime.lua.set_app_data(SiteState::new(module));
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
    pub fn module(&self) -> Module {
        self.state::<SiteState>().module.clone()
    }

    /// Reports whether the module implements `event`.
    #[must_use]
    pub fn has_handler(&self, event: Event) -> bool {
        self.state::<SiteState>().module.handler(event).is_some()
    }

    /// Changes a setting the module declares.
    ///
    /// # Errors
    ///
    /// Fails if the module declares no such setting, or the value does not
    /// suit it: a checkbox takes a flag, a number field a whole number, a
    /// choice the index of one of its items, and a text field text.
    pub fn set_option(&self, name: &str, value: OptionValue) -> Result<()> {
        let mut site = self.state::<SiteState>();
        let module = &site.module;
        let Some(option) = module.options.iter().find(|o| o.name == name) else {
            let message = format!("module {} has no setting named {name:?}", module.name);
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
    pub fn set_account(&self, username: &str, password: &str) {
        let mut account = self.state::<Account>();
        account.enabled = !username.is_empty();
        username.clone_into(&mut account.username);
        password.clone_into(&mut account.password);
    }

    /// Returns the login state the module recorded.
    #[must_use]
    pub fn account_status(&self) -> AccountStatus {
        self.state::<Account>().status
    }

    /// Selects which of the site's directory listings to walk next, counting
    /// from 0.
    ///
    /// # Errors
    ///
    /// Fails if the site has no listing with that index.
    pub fn select_directory(&self, index: u32) -> Result<()> {
        let mut site = self.state::<SiteState>();
        if index >= site.module.directories {
            let message = format!(
                "module {} has {} directory listings, not {}",
                site.module.name,
                site.module.directories,
                index + 1
            );
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        site.directory_index = i64::from(index);
        Ok(())
    }

    /// Returns what the last request the module made got back.
    #[must_use]
    pub fn last_request(&self) -> LastRequest {
        self.state::<HttpSession>().last.clone()
    }

    /// Runs `OnGetDirectoryPageNumber`, returning how many pages the selected
    /// directory listing has. A module without the handler has one.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails.
    pub fn directory_pages(&self) -> Result<u32> {
        if !self.has_handler(Event::GetDirectoryPageNumber) {
            return Ok(1);
        }
        globals::set_page_count(&self.runtime.lua, 1)?;
        self.call(Event::GetDirectoryPageNumber)?;
        let pages = globals::page_count(&self.runtime.lua)?;
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
    pub fn info(&self, series_url: &str) -> Result<SeriesInfo> {
        *self.state::<MangaInfo>() = MangaInfo::for_url(series_url);
        globals::set_url(&self.runtime.lua, series_url)?;
        let code = to_int(&self.runtime.lua, &self.call(Event::GetInfo)?);
        let module = self.module().name;
        match code {
            globals::NET_PROBLEM => {
                let what = format!("fetching {series_url}");
                return Err(Error::network(&module, &what, self.last_request()));
            }
            globals::INFORMATION_NOT_FOUND => {
                let message = format!(
                    "{module}: no series information at {series_url}; the page may have moved"
                );
                return Err(Error::new(ErrorKind::NotFound, message));
            }
            _ => {}
        }
        Ok(std::mem::take(&mut *self.state::<MangaInfo>()).into_series())
    }

    /// Runs `OnGetPageNumber` for the chapter at `chapter_url`, returning its
    /// pages in reading order.
    ///
    /// Image addresses are made absolute against the site's address. A page
    /// the module finds only when asked is [`Page::Unresolved`]; resolve it
    /// with [`resolve_page`](Self::resolve_page).
    ///
    /// # Errors
    ///
    /// Fails if the module has no `OnGetPageNumber` handler, the handler fails
    /// or declines, a request it needed failed
    /// ([`Network`](ErrorKind::Network)), or it lists no pages
    /// ([`NotFound`](ErrorKind::NotFound)).
    pub fn pages(&self, chapter_url: &str) -> Result<Vec<Page>> {
        *self.state::<Task>() = Task::for_chapter(chapter_url);
        globals::set_url(&self.runtime.lua, chapter_url)?;
        let result = self.call(Event::GetPageNumber)?;
        self.check_outcome(&result, &format!("reading pages for {chapter_url}"))?;

        let module = self.module();
        let resolvable = self.has_handler(Event::GetImageUrl);
        let pages = self.state::<Task>().pages(&module.root_url, resolvable);
        if pages.is_empty() {
            let message = format!("{}: no pages listed for {chapter_url}", module.name);
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
    pub fn resolve_page(&self, chapter_url: &str, index: usize) -> Result<String> {
        let module = self.module();
        if !self.state::<Task>().is_for(chapter_url) {
            let message = format!("list the pages of {chapter_url} before resolving one");
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        if let Page::Image(url) = self.state::<Task>().page(&module.root_url, index) {
            return Ok(url);
        }
        globals::set_url(&self.runtime.lua, chapter_url)?;
        globals::set_work_id(&self.runtime.lua, index)?;
        let what = format!("resolving page {} of {chapter_url}", index + 1);
        let result = self.call(Event::GetImageUrl)?;
        self.check_outcome(&result, &what)?;
        match self.state::<Task>().page(&module.root_url, index) {
            Page::Image(url) => Ok(url),
            Page::Unresolved => {
                let message = format!("{}: no image address after {what}", module.name);
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
        let module = self.module().name;
        let net_problem = globals::NET_PROBLEM;
        match result {
            Value::Nil | Value::Boolean(false) => {
                let status = self.last_request().status;
                let message = format!("{module}: declined {what} (last status {status})");
                Err(Error::new(ErrorKind::Module, message))
            }
            Value::Integer(code) if *code == net_problem => {
                Err(Error::network(&module, what, self.last_request()))
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
    pub fn login(&self) -> Result<bool> {
        let module = self.module().name;
        if !self.has_handler(Event::Login) {
            return Err(no_handler(&module, Event::Login));
        }
        if !self.state::<Account>().enabled {
            let message = format!("module {module} has no credentials configured");
            return Err(Error::new(ErrorKind::Unsupported, message));
        }
        let accepted = truthy(&self.call(Event::Login)?);
        if self.account_status() == AccountStatus::Invalid {
            let message = format!("module {module} rejected the credentials");
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
    pub fn before_download_image(&self, image_url: &str) -> Result<HashMap<String, String>> {
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
    pub fn download_image(&self, image_url: &str) -> Result<Vec<u8>> {
        self.state::<HttpSession>().document.clear();
        globals::set_url(&self.runtime.lua, image_url)?;
        let module = self.module().name;
        if !truthy(&self.call(Event::DownloadImage)?) {
            let message = format!("{module}: module declined to download {image_url}");
            return Err(Error::new(ErrorKind::Module, message));
        }
        let data = std::mem::take(&mut self.state::<HttpSession>().document);
        if data.is_empty() {
            let message = format!("{module}: module returned no image data for {image_url}");
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
    pub fn after_image_saved(&self, path: &Path) -> Result<()> {
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
        let module = self.module();
        let Some(function) = module.handler(event).map(str::to_owned) else {
            return Err(no_handler(&module.name, event));
        };
        let Value::Function(handler) = self.runtime.lua.globals().get(function.as_str())? else {
            let message = format!(
                "module {} declares {event}={function:?} but defines no such function",
                module.name
            );
            return Err(Error::new(ErrorKind::Module, message));
        };
        let context = format!("{}/{function}", module.name);
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
        let mut site = self.state::<SiteState>();
        site.module.handlers.insert(event, function.to_owned());
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
fn no_handler(module: &str, event: Event) -> Error {
    Error::new(
        ErrorKind::Unsupported,
        format!("module {module} has no {event} handler"),
    )
}

#[cfg(test)]
mod tests;
