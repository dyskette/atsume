//! Loading a module file and running its handlers.

use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::rc::Rc;

use mlua::{AppDataRefMut, Function, Lua, Value};

use crate::bind::state;
use crate::budget::{Budget, CancelToken, DEFAULT_CPU_LIMIT, Termination};
use crate::convert::truthy;
use crate::error::{Error, Result};
use crate::http::{HttpConfig, HttpObject, HttpSession, LastRequest};
use crate::module::{self, Event, Module, ModuleOption, OptionKind, OptionValue, Storage};
use crate::sandbox::{self, WritableFile};
use crate::site::{Account, ModuleObject, SiteState};
use crate::{globals, libs};

pub use crate::site::AccountStatus;

/// Loads website modules from a checkout of FMD2's `lua` directory.
///
/// The checkout is cloned at run time and never built in: the modules are
/// GPL-2.0-only, which is incompatible with atsume. See `docs/MODULES.md`.
#[derive(Clone)]
pub struct Host {
    /// The `lua` directory of an FMD2 checkout.
    pub lua_dir: PathBuf,
    /// How runners reach the network.
    pub http: HttpConfig,
}

/// One module file loaded into its own Lua runtime, for one of its websites.
///
/// A runtime is single-threaded and the module API is built on globals, so
/// each scrape opens its own runner. Its state lives in the runtime, where the
/// objects modules see read and write it.
pub struct Runner {
    lua: Lua,
    budget: Rc<Budget>,
    sites: Vec<Module>,
}

impl Host {
    /// Returns a host for the checkout at `lua_dir` that reaches the network
    /// directly, without rate limits.
    #[must_use]
    pub fn new(lua_dir: impl Into<PathBuf>) -> Self {
        Self {
            lua_dir: lua_dir.into(),
            http: HttpConfig::default(),
        }
    }

    /// Loads `file` and prepares a runner for one of the websites it declares.
    ///
    /// A file may declare several websites, or one site under several
    /// addresses. `site` selects by name, and the first declaration otherwise.
    /// `root_url` picks between mirrors; an address the site does not declare
    /// replaces its own, since it is one the reader typed for a site that
    /// moved.
    ///
    /// # Errors
    ///
    /// Fails if the file does not load, `Init()` raises or declares nothing,
    /// no website matches `site`, or `cancel` is already cancelled.
    pub fn open(
        &self,
        file: &Path,
        site: Option<&str>,
        root_url: Option<&str>,
        cancel: CancelToken,
    ) -> Result<Runner> {
        if cancel.is_cancelled() {
            return Err(Error::Cancelled);
        }
        let (lua, budget) = self.runtime(cancel)?;
        let sites = load_declarations(&lua, &budget, file)?;
        let Some(index) = select_site(&sites, site, root_url) else {
            let name = site.unwrap_or_default().to_owned();
            return Err(Error::NoSuchSite {
                file: file.to_path_buf(),
                name,
            });
        };
        let mut selected = sites[index].clone();
        if let Some(root_url) = root_url {
            root_url.clone_into(&mut selected.root_url);
        }
        lua.set_app_data(SiteState::new(selected));
        lua.globals().set("MODULE", ModuleObject)?;
        Ok(Runner { lua, budget, sites })
    }

    /// Builds the sandbox with the budget, the `fmd.*` libraries, the state a
    /// scrape keeps and the globals a module expects before its file runs.
    fn runtime(&self, cancel: CancelToken) -> Result<(Lua, Rc<Budget>)> {
        let lua = sandbox::new_runtime(&self.lua_dir)?;
        let budget = Budget::new(cancel.clone(), DEFAULT_CPU_LIMIT);
        budget.install(&lua)?;
        globals::set_constants(&lua)?;
        let lua_dir = std::path::absolute(&self.lua_dir).map_err(mlua::Error::external)?;
        libs::preload(&lua, &lua_dir)?;
        lua.set_app_data(Storage::default());
        lua.set_app_data(Account::default());
        lua.set_app_data(HttpSession::new(&self.http, cancel));
        lua.globals().set("HTTP", HttpObject)?;
        module::register_declarations(&lua)?;
        Ok((lua, budget))
    }
}

/// Runs a module file and its `Init()`, and returns what it declared.
fn load_declarations(lua: &Lua, budget: &Rc<Budget>, file: &Path) -> Result<Vec<Module>> {
    run_file(lua, budget, file)?;
    let init_result = protect(lua, budget, |lua| {
        lua.globals().get::<Function>("Init")?.call::<Value>(())
    })
    .map_err(|e| Error::Init {
        file: file.to_path_buf(),
        source: Box::new(e),
    })?;
    let declared = module::take_declarations(lua, init_result, file)?;
    declared.ok_or_else(|| Error::NoDeclaration {
        file: file.to_path_buf(),
    })
}

/// Loads and runs a module file's top level.
fn run_file(lua: &Lua, budget: &Rc<Budget>, file: &Path) -> Result<()> {
    let wrap = |e| Error::Load {
        file: file.to_path_buf(),
        source: Box::new(e),
    };
    let src = std::fs::read(file).map_err(|source| Error::Io {
        path: file.to_path_buf(),
        source,
    });
    let src = src.map_err(wrap)?;
    protect(lua, budget, |lua| {
        sandbox::load_text(lua, file, &src)?.call::<()>(())
    })
    .map_err(wrap)
}

/// Runs one call into the module under the CPU budget, and not at all once
/// the scrape is cancelled.
///
/// A call cancelled mid-way returns [`Error::Cancelled`] even if the module
/// caught the termination and returned, so its result is never taken as real.
fn protect<R>(
    lua: &Lua,
    budget: &Rc<Budget>,
    call: impl FnOnce(&Lua) -> mlua::Result<R>,
) -> Result<R> {
    if budget.cancel_token().is_cancelled() {
        return Err(Error::Cancelled);
    }
    budget.start(lua)?;
    let result = call(lua);
    let termination = budget.finish();
    if budget.cancel_token().is_cancelled() {
        return Err(Error::Cancelled);
    }
    match termination {
        Some(Termination::Cancelled) => Err(Error::Cancelled),
        Some(Termination::CpuLimit) => Err(Error::CpuLimit {
            limit: budget.limit(),
        }),
        None => result.map_err(Error::Lua),
    }
}

/// Picks the website a caller asked for, returning its index.
///
/// No name takes the first declaration. Mirrors share a name, so `root_url`
/// breaks the tie, and an address no mirror declares takes the first.
fn select_site(sites: &[Module], name: Option<&str>, root_url: Option<&str>) -> Option<usize> {
    let Some(name) = name.filter(|n| !n.is_empty()) else {
        return (!sites.is_empty()).then_some(0);
    };
    let mut matching = (0..sites.len()).filter(|&i| sites[i].name.eq_ignore_ascii_case(name));
    let first = matching.next()?;
    let Some(root_url) = root_url else {
        return Some(first);
    };
    let mirror = std::iter::once(first)
        .chain(matching)
        .find(|&i| sites[i].root_url.eq_ignore_ascii_case(root_url));
    Some(mirror.unwrap_or(first))
}

impl Runner {
    /// Borrows state the runtime keeps, between calls into the module.
    ///
    /// Every state value is set when the runner is built, and only a binding
    /// running inside a call holds one, so no borrow can be outstanding here.
    #[expect(
        clippy::expect_used,
        reason = "the runner sets every state value before handing itself out, and calls into it never overlap a binding"
    )]
    fn state<S: 'static>(&self) -> AppDataRefMut<'_, S> {
        state::<S>(&self.lua).expect("runner state is set and not in use")
    }

    /// Returns the selected website as its declaration stands now.
    #[must_use]
    pub fn module(&self) -> Module {
        self.state::<SiteState>().module.clone()
    }

    /// Returns every website the file declared, as `Init()` left them.
    #[must_use]
    pub fn sites(&self) -> &[Module] {
        &self.sites
    }

    /// Returns the settings the selected website declares.
    #[must_use]
    pub fn options(&self) -> Vec<ModuleOption> {
        self.state::<SiteState>().module.options.clone()
    }

    /// Reports whether the module implements `event`.
    #[must_use]
    pub fn has_handler(&self, event: Event) -> bool {
        self.state::<SiteState>().module.handler(event).is_some()
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

    /// Selects which listing a site with several walks next.
    pub fn set_directory_index(&self, index: i64) {
        self.state::<SiteState>().directory_index = index;
    }

    /// Returns how many separate listings the site's directory is split into.
    #[must_use]
    pub fn directories(&self) -> u32 {
        self.state::<SiteState>().module.directories
    }

    /// Stores a setting from its text form, converted to the type the module
    /// declared, so a number compares as a number in Lua.
    pub fn set_option_text(&self, name: &str, value: &str) {
        let mut site = self.state::<SiteState>();
        let kind = site
            .module
            .options
            .iter()
            .find(|o| o.name == name)
            .map(|o| o.kind);
        let value = match kind {
            Some(OptionKind::CheckBox) => {
                OptionValue::Bool(value == "1" || value.eq_ignore_ascii_case("true"))
            }
            Some(OptionKind::SpinEdit | OptionKind::ComboBox) => value
                .trim()
                .parse()
                .map_or_else(|_| OptionValue::Text(value.to_owned()), OptionValue::Int),
            _ => OptionValue::Text(value.to_owned()),
        };
        site.options.insert(name.to_owned(), value);
    }

    /// Sets the instruction budget for each later call into the module.
    pub fn set_cpu_limit(&self, instructions: u64) {
        self.budget.set_limit(instructions);
    }

    /// Returns the most instructions any one call has used so far.
    #[must_use]
    pub fn peak_instructions(&self) -> u64 {
        self.budget.peak()
    }

    /// Returns what the last request the module made got back.
    #[must_use]
    pub fn last_request(&self) -> LastRequest {
        self.state::<HttpSession>().last.clone()
    }

    /// Runs `OnGetDirectoryPageNumber`, returning how many directory pages the
    /// site has. A module without the handler has one.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails.
    pub fn directory_page_count(&self) -> Result<i64> {
        if !self.has_handler(Event::GetDirectoryPageNumber) {
            return Ok(1);
        }
        globals::set_page_count(&self.lua, 1)?;
        self.call(Event::GetDirectoryPageNumber)?;
        Ok(globals::page_count(&self.lua)?.max(1))
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
            return Err(Error::NoHandler {
                module,
                event: Event::Login,
            });
        }
        if !self.state::<Account>().enabled {
            return Err(Error::NoCredentials { module });
        }
        let accepted = truthy(&self.call(Event::Login)?);
        if self.account_status() == AccountStatus::Invalid {
            return Err(Error::CredentialsRejected { module });
        }
        Ok(accepted)
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
        globals::set_filename(&self.lua, path)?;
        self.state::<WritableFile>().0 = Some(path.to_path_buf());
        let result = self.call(Event::AfterImageSaved);
        self.state::<WritableFile>().0 = None;
        result.map(|_| ())
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
        globals::set_url(&self.lua, image_url)?;
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
        globals::set_url(&self.lua, image_url)?;
        let module = self.module().name;
        let url = image_url.to_owned();
        if !truthy(&self.call(Event::DownloadImage)?) {
            return Err(Error::DownloadDeclined { module, url });
        }
        let data = std::mem::take(&mut self.state::<HttpSession>().document);
        if data.is_empty() {
            return Err(Error::EmptyImage { module, url });
        }
        Ok(data)
    }

    /// Calls the Lua function bound to `event` and returns its result.
    pub(crate) fn call(&self, event: Event) -> Result<Value> {
        let module = self.module();
        let Some(function) = module.handler(event).map(str::to_owned) else {
            return Err(Error::NoHandler {
                module: module.name,
                event,
            });
        };
        let Value::Function(handler) = self.lua.globals().get(function.as_str())? else {
            return Err(Error::MissingFunction {
                module: module.name,
                event,
                function,
            });
        };
        protect(&self.lua, &self.budget, |_| handler.call::<Value>(())).map_err(|e| {
            Error::Handler {
                call: format!("{}/{function}", module.name),
                source: Box::new(e),
            }
        })
    }

    /// Points `event` at another Lua function, for tests.
    #[cfg(test)]
    pub(crate) fn set_handler(&self, event: Event, function: &str) {
        self.state::<SiteState>()
            .module
            .handlers
            .insert(event, function.to_owned());
    }

    /// Runs Lua statements in the runner's globals, for tests.
    #[cfg(test)]
    pub(crate) fn exec(&self, statements: &str) {
        self.lua.load(statements).exec().unwrap();
    }

    /// Evaluates a Lua expression in the runner's globals and returns it as
    /// text, for tests to look inside a runner.
    #[cfg(test)]
    pub(crate) fn eval(&self, expression: &str) -> String {
        let value: Value = self
            .lua
            .load(format!("return {expression}"))
            .eval()
            .unwrap();
        let text = self.lua.coerce_string(value).unwrap();
        text.map(|s| s.to_string_lossy()).unwrap_or_default()
    }

    /// Returns the runner's Lua runtime, for tests to define helpers in.
    #[cfg(test)]
    pub(crate) fn lua(&self) -> &Lua {
        &self.lua
    }
}

#[cfg(test)]
mod tests;
