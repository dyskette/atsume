//! Preparing a Lua runtime for a module and running calls into it.

use std::path::Path;
use std::rc::Rc;

use mlua::{Function, Lua, Value};

use crate::budget::{Budget, Termination};
use crate::cancel::CancelToken;
use crate::error::{Error, ErrorKind, Result};
use crate::http::{HttpObject, HttpSession};
use crate::module::{self, Module, Storage};
use crate::session::SessionConfig;
use crate::site::Account;
use crate::{globals, libs, sandbox, series};

/// A sandboxed runtime holding the state a scrape keeps, with every call
/// into it bounded by the CPU budget and the scrape's cancellation.
pub(crate) struct Runtime {
    pub(crate) lua: Lua,
    pub(crate) budget: Rc<Budget>,
}

impl Runtime {
    /// Builds the sandbox with the budget, the `fmd.*` libraries, the state a
    /// scrape keeps and the globals a module expects before its file runs.
    pub(crate) fn new(lua_dir: &Path, config: &SessionConfig, cancel: CancelToken) -> Result<Self> {
        if cancel.is_cancelled() {
            return Err(Error::cancelled());
        }
        let lua = sandbox::new_runtime(lua_dir)?;
        let budget = Budget::new(cancel.clone(), config.cpu_limit);
        budget.install(&lua)?;
        globals::set_constants(&lua)?;
        let lua_dir = std::path::absolute(lua_dir).map_err(mlua::Error::external)?;
        libs::preload(&lua, &lua_dir)?;
        lua.set_app_data(Storage::default());
        lua.set_app_data(Account::default());
        lua.set_app_data(HttpSession::new(&config.http, cancel));
        lua.globals().set("HTTP", HttpObject)?;
        series::register(&lua)?;
        module::register_declarations(&lua)?;
        Ok(Self { lua, budget })
    }

    /// Runs a module file and its `Init()`, and returns what it declared.
    pub(crate) fn load(&self, file: &Path) -> Result<Vec<Module>> {
        self.run_file(file)?;
        let init_result = self
            .protect(|lua| lua.globals().get::<Function>("Init")?.call::<Value>(()))
            .map_err(|e| e.within(format_args!("Init {}", file.display()), ErrorKind::Load))?;
        let declared = module::take_declarations(&self.lua, init_result, file)?;
        declared.ok_or_else(|| {
            let message = format!("{}: Init never called NewWebsiteModule", file.display());
            Error::new(ErrorKind::Load, message)
        })
    }

    /// Loads and runs a module file's top level.
    fn run_file(&self, file: &Path) -> Result<()> {
        let context = format!("load {}", file.display());
        let src = std::fs::read(file)
            .map_err(|e| Error::new(ErrorKind::Load, format!("{context}: {e}")))?;
        self.protect(|lua| sandbox::load_text(lua, file, &src)?.call::<()>(()))
            .map_err(|e| e.within(context, ErrorKind::Load))
    }

    /// Runs one call into the module under the CPU budget, and not at all once
    /// the scrape is cancelled.
    ///
    /// A call cancelled mid-way is a cancellation even if the module caught
    /// the termination and returned, so its result is never taken as real.
    pub(crate) fn protect<R>(&self, call: impl FnOnce(&Lua) -> mlua::Result<R>) -> Result<R> {
        let cancelled = || self.budget.cancel_token().is_cancelled();
        if cancelled() {
            return Err(Error::cancelled());
        }
        self.budget.start(&self.lua)?;
        let result = call(&self.lua);
        let termination = self.budget.finish();
        if cancelled() {
            return Err(Error::cancelled());
        }
        match termination {
            Some(Termination::Cancelled) => Err(Error::cancelled()),
            Some(Termination::CpuLimit) => Err(Error::cpu_limit(self.budget.limit())),
            None => result.map_err(Error::from),
        }
    }
}
