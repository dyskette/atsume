//! One scrape of one website.

use std::collections::HashMap;
use std::path::Path;

use mlua::{AppDataRefMut, Value};

use crate::bind::state;
use crate::convert::truthy;
use crate::error::{Error, ErrorKind, Result};
use crate::globals;
use crate::http::{HttpSession, LastRequest};
use crate::module::{Event, Module, OptionKind, OptionValue};
use crate::runtime::Runtime;
use crate::sandbox::WritableFile;
use crate::site::{Account, AccountStatus, ModuleObject, SiteState};

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

    /// Sets the instruction budget for each later call into the module.
    pub fn set_cpu_limit(&self, instructions: u64) {
        self.runtime.budget.set_limit(instructions);
    }

    /// Returns the most instructions any one call has used so far.
    #[must_use]
    pub fn peak_instructions(&self) -> u64 {
        self.runtime.budget.peak()
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
