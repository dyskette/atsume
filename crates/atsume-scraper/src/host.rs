//! Loading a module file and running its handlers.

use std::cell::RefCell;
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::rc::Rc;

use mlua::{Function, Lua, Table, Value};

use crate::budget::{Budget, CancelToken, DEFAULT_CPU_LIMIT, Termination};
use crate::error::{Error, Result};
use crate::libs;
use crate::module::{
    self, Module, ModuleOption, OptionKind, OptionValue, Options, Storage, StorageHandle,
};
use crate::object::{Field, Object, to_int, truthy};
use crate::sandbox::{self, WritableFile};

/// Handler return codes, which modules read as globals.
const NO_ERROR: i64 = 0;
const NET_PROBLEM: i64 = 1;
const INFORMATION_NOT_FOUND: i64 = 2;

/// Loads website modules from a checkout of FMD2's `lua` directory.
///
/// The checkout is cloned at run time and never built in: the modules are
/// GPL-2.0-only, which is incompatible with atsume. See `docs/MODULES.md`.
#[derive(Clone, Debug)]
pub struct Host {
    /// The `lua` directory of an FMD2 checkout.
    pub lua_dir: PathBuf,
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

impl AccountStatus {
    fn code(self) -> i64 {
        match self {
            Self::Unknown => -1,
            Self::Checking => 0,
            Self::Valid => 1,
            Self::Invalid => 2,
        }
    }

    fn from_code(code: i64) -> Self {
        match code {
            0 => Self::Checking,
            1 => Self::Valid,
            2 => Self::Invalid,
            _ => Self::Unknown,
        }
    }
}

/// The credentials a module sees as `MODULE.Account`.
#[derive(Clone, Debug, Default)]
pub struct Account {
    /// Whether credentials were supplied. Login handlers check this first.
    pub enabled: bool,
    /// The login name.
    pub username: String,
    /// The password.
    pub password: String,
    /// Cookies the module saved for the session.
    pub cookies: String,
    /// What the module recorded about the login.
    pub status: AccountStatus,
}

/// The selected website and the state its `MODULE` object exposes.
struct SiteState {
    module: Module,
    options: HashMap<String, OptionValue>,
    directory_index: i64,
}

/// One module file loaded into its own Lua runtime, for one of its websites.
///
/// A runtime is single-threaded and the module API is built on globals, so
/// each scrape opens its own runner.
pub struct Runner {
    lua: Lua,
    budget: Rc<Budget>,
    site: Rc<RefCell<SiteState>>,
    account: Rc<RefCell<Account>>,
    sites: Vec<Module>,
    writable: WritableFile,
}

impl Host {
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
        let writable = WritableFile::default();
        let lua = sandbox::new_runtime(&self.lua_dir, Rc::clone(&writable))?;
        let budget = Budget::new(cancel, DEFAULT_CPU_LIMIT);
        budget.install(&lua)?;
        set_constants(&lua)?;
        let lua_dir = std::path::absolute(&self.lua_dir).map_err(mlua::Error::external)?;
        libs::preload(&lua, &lua_dir)?;

        let storage = Storage::default();
        let declarations = Declarations::default();
        declarations.register(&lua, &storage)?;
        run_file(&lua, &budget, file)?;
        let init_result = protect(&lua, &budget, |lua| {
            lua.globals().get::<Function>("Init")?.call::<Value>(())
        })
        .map_err(|e| Error::Init {
            file: file.to_path_buf(),
            source: Box::new(e),
        })?;
        let sites = declarations.read(&lua, init_result, file)?;

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

        let runner = Runner {
            site: Rc::new(RefCell::new(SiteState::new(selected))),
            account: Rc::default(),
            lua,
            budget,
            sites,
            writable,
        };
        runner.bind_module(&storage)?;
        Ok(runner)
    }
}

/// Sets the globals every module reads: return codes and account states.
fn set_constants(lua: &Lua) -> mlua::Result<()> {
    let globals = lua.globals();
    globals.set("no_error", NO_ERROR)?;
    globals.set("net_problem", NET_PROBLEM)?;
    globals.set("information_not_found", INFORMATION_NOT_FOUND)?;
    for status in [
        AccountStatus::Unknown,
        AccountStatus::Checking,
        AccountStatus::Valid,
        AccountStatus::Invalid,
    ] {
        globals.set(format!("as{status:?}"), status.code())?;
    }
    globals.set("PAGENUMBER", 1)
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

/// The tables `NewWebsiteModule()` handed out while the file ran.
#[derive(Clone, Default)]
struct Declarations(Rc<RefCell<Vec<(Table, Options)>>>);

impl Declarations {
    /// Defines `NewWebsiteModule`, recording each table it returns.
    ///
    /// Each declaration collects its own options, so two sites in one file do
    /// not show each other's settings.
    fn register(&self, lua: &Lua, storage: &Storage) -> mlua::Result<()> {
        let storage = Value::UserData(lua.create_userdata(StorageHandle(Rc::clone(storage)))?);
        let declarations = self.clone();
        let new_website_module = lua.create_function(move |lua, ()| {
            let options = Options::default();
            let table = module::declaration_table(lua, &options, storage.clone())?;
            declarations.0.borrow_mut().push((table.clone(), options));
            Ok(table)
        })?;
        lua.globals().set("NewWebsiteModule", new_website_module)
    }

    /// Reads every declaration into a [`Module`], in declaration order.
    ///
    /// Most modules fill the table and fall off the end of `Init()`; a few
    /// return it instead, which is the same single declaration.
    fn read(&self, lua: &Lua, init_result: Value, file: &Path) -> Result<Vec<Module>> {
        let mut declarations = self.0.borrow_mut();
        if let (Value::Table(table), true) = (init_result, declarations.is_empty()) {
            declarations.push((table, Options::default()));
        }
        if declarations.is_empty() {
            return Err(Error::NoDeclaration {
                file: file.to_path_buf(),
            });
        }
        let mut sites = Vec::with_capacity(declarations.len());
        for (table, options) in declarations.iter() {
            let options = options.borrow().clone();
            sites.push(module::read_declaration(lua, table, options, file)?);
        }
        Ok(sites)
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

impl SiteState {
    fn new(module: Module) -> Self {
        let options = module
            .options
            .iter()
            .filter_map(|o| Some((o.name.clone(), o.default.clone()?)))
            .collect();
        Self {
            module,
            options,
            directory_index: 0,
        }
    }
}

impl Runner {
    /// Returns the selected website as its declaration stands now.
    #[must_use]
    pub fn module(&self) -> Module {
        self.site.borrow().module.clone()
    }

    /// Returns every website the file declared, as `Init()` left them.
    #[must_use]
    pub fn sites(&self) -> &[Module] {
        &self.sites
    }

    /// Returns the settings the selected website declares.
    #[must_use]
    pub fn options(&self) -> Vec<ModuleOption> {
        self.site.borrow().module.options.clone()
    }

    /// Reports whether the module implements `event`.
    #[must_use]
    pub fn has_handler(&self, event: &str) -> bool {
        self.site.borrow().module.handler(event).is_some()
    }

    /// Supplies credentials for a module that implements `OnLogin`.
    pub fn set_account(&self, username: &str, password: &str) {
        let mut account = self.account.borrow_mut();
        account.enabled = !username.is_empty();
        username.clone_into(&mut account.username);
        password.clone_into(&mut account.password);
    }

    /// Returns the login state the module recorded.
    #[must_use]
    pub fn account_status(&self) -> AccountStatus {
        self.account.borrow().status
    }

    /// Selects which listing a site with several walks next.
    pub fn set_directory_index(&self, index: i64) {
        self.site.borrow_mut().directory_index = index;
    }

    /// Returns how many separate listings the site's directory is split into.
    #[must_use]
    pub fn total_directories(&self) -> i64 {
        self.site.borrow().module.total_directory.max(1)
    }

    /// Stores a setting from its text form, converted to the type the module
    /// declared, so a number compares as a number in Lua.
    pub fn set_option_text(&self, name: &str, value: &str) {
        let mut site = self.site.borrow_mut();
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

    /// Runs `OnGetDirectoryPageNumber`, returning how many directory pages the
    /// site has. A module without the handler has one.
    ///
    /// # Errors
    ///
    /// Fails if the handler fails.
    pub fn directory_page_count(&self) -> Result<i64> {
        if !self.has_handler("OnGetDirectoryPageNumber") {
            return Ok(1);
        }
        self.lua.globals().set("PAGENUMBER", 1)?;
        self.call("OnGetDirectoryPageNumber")?;
        let pages = to_int(&self.lua, &self.lua.globals().get("PAGENUMBER")?);
        Ok(pages.max(1))
    }

    /// Runs `OnLogin`, reporting whether the site accepted the credentials.
    ///
    /// # Errors
    ///
    /// Fails if the module has no login handler, no credentials were set, the
    /// handler fails, or the module recorded the credentials as invalid even
    /// though the handler succeeded.
    pub fn login(&self) -> Result<bool> {
        let module = self.site.borrow().module.name.clone();
        if !self.has_handler("OnLogin") {
            return Err(Error::NoHandler {
                module,
                event: "OnLogin".to_owned(),
            });
        }
        if !self.account.borrow().enabled {
            return Err(Error::NoCredentials { module });
        }
        let accepted = truthy(&self.call("OnLogin")?);
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
        if !self.has_handler("OnAfterImageSaved") {
            return Ok(());
        }
        let lua_path = self.lua.create_string(path.to_string_lossy().as_bytes())?;
        self.lua.globals().set("FILENAME", lua_path)?;
        *self.writable.borrow_mut() = Some(path.to_path_buf());
        let result = self.call("OnAfterImageSaved");
        *self.writable.borrow_mut() = None;
        result.map(|_| ())
    }

    /// Calls the Lua function bound to `event` and returns its result.
    pub(crate) fn call(&self, event: &str) -> Result<Value> {
        let (module, function) = {
            let site = self.site.borrow();
            let name = site.module.name.clone();
            let Some(function) = site.module.handler(event).map(str::to_owned) else {
                return Err(Error::NoHandler {
                    module: name,
                    event: event.to_owned(),
                });
            };
            (name, function)
        };
        let Value::Function(handler) = self.lua.globals().get(function.as_str())? else {
            let event = event.to_owned();
            return Err(Error::MissingFunction {
                module,
                event,
                function,
            });
        };
        protect(&self.lua, &self.budget, |_| handler.call::<Value>(())).map_err(|e| {
            Error::Handler {
                call: format!("{module}/{function}"),
                source: Box::new(e),
            }
        })
    }

    /// Defines the `MODULE` global for the selected website.
    fn bind_module(&self, storage: &Storage) -> mlua::Result<()> {
        let lua = &self.lua;
        let storage = lua.create_userdata(StorageHandle(Rc::clone(storage)))?;
        let module = Object::new(Rc::clone(&self.site))
            .field("ID", Field::Text(|s| &mut s.module.id))
            .field("Name", Field::Text(|s| &mut s.module.name))
            .field("RootURL", Field::Text(|s| &mut s.module.root_url))
            .field("Category", Field::Text(|s| &mut s.module.category))
            .field(
                "TotalDirectory",
                Field::Int(|s| &mut s.module.total_directory),
            )
            .field(
                "AccountSupport",
                Field::Bool(|s| &mut s.module.account_support),
            )
            .field(
                "CurrentDirectoryIndex",
                Field::Int(|s| &mut s.directory_index),
            )
            .field(
                "GetOption",
                Field::Constant(Value::Function(self.get_option()?)),
            )
            .field("Storage", Field::Constant(Value::UserData(storage)))
            .field(
                "Account",
                Field::Constant(bind_account(lua, &self.account)?),
            )
            .field("ActiveConnectionCount", Field::Constant(Value::Integer(1)));
        lua.globals().set("MODULE", module.into_value(lua)?)
    }

    /// Builds `MODULE.GetOption(name)`, which returns the setting's value, or
    /// `nil` for one that has none.
    fn get_option(&self) -> mlua::Result<Function> {
        let site = Rc::clone(&self.site);
        self.lua.create_function(move |lua, name: String| {
            let site = site.borrow();
            site.options
                .get(&name)
                .map_or(Ok(Value::Nil), |value| value.to_lua(lua))
        })
    }
}

/// Builds `MODULE.Account`.
fn bind_account(lua: &Lua, account: &Rc<RefCell<Account>>) -> mlua::Result<Value> {
    let status = Field::Custom {
        get: status_code,
        set: set_status,
    };
    Object::new(Rc::clone(account))
        .field("Enabled", Field::Bool(|a| &mut a.enabled))
        .field("Username", Field::Text(|a| &mut a.username))
        .field("Password", Field::Text(|a| &mut a.password))
        .field("Cookies", Field::Text(|a| &mut a.cookies))
        .field("Status", status)
        .into_value(lua)
}

fn status_code(account: &Account) -> Value {
    Value::Integer(account.status.code())
}

fn set_status(lua: &Lua, account: &mut Account, value: &Value) {
    account.status = AccountStatus::from_code(to_int(lua, value));
}

#[cfg(test)]
mod tests;
