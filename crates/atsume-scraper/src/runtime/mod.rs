//! Prepares a Lua runtime for a module and runs calls into it.

pub(crate) mod bind;
pub(crate) mod budget;
pub(crate) mod convert;
pub(crate) mod sandbox;

use std::path::Path;
use std::rc::Rc;

use mlua::{Function, Lua, Value};

use crate::cancel::CancelToken;
use crate::error::{Error, ErrorKind, Result};
use crate::http::HttpSession;
use crate::model::SiteDeclaration;
use crate::session::SessionConfig;
use crate::upstream::{self, declaration};
use budget::{Budget, Termination};

/// A sandboxed Lua runtime that holds the state a scrape keeps. The CPU budget
/// and the scrape's cancellation bound every call into it.
pub(crate) struct Runtime {
    pub(crate) lua: Lua,
    pub(crate) budget: Rc<Budget>,
}

impl Runtime {
    /// Builds the sandbox, and installs the CPU budget, the `fmd.*` libraries,
    /// the state a scrape keeps, and the globals that a module expects before
    /// its file runs.
    pub(crate) fn new(lua_dir: &Path, config: &SessionConfig, cancel: CancelToken) -> Result<Self> {
        if cancel.is_cancelled() {
            return Err(Error::cancelled());
        }
        let lua = sandbox::new_sandboxed_lua(lua_dir)?;
        let budget = Budget::new(cancel.clone(), config.cpu_limit);
        budget.install(&lua)?;
        let lua_dir = std::path::absolute(lua_dir).map_err(mlua::Error::external)?;
        let http = HttpSession::new(&config.http, cancel.clone());
        upstream::install(&lua, &lua_dir, http, cancel)?;
        Ok(Self { lua, budget })
    }

    /// Runs a module file and its `Init()`, and returns the websites that the
    /// file declared.
    pub(crate) fn load_declarations(&self, file: &Path) -> Result<Vec<SiteDeclaration>> {
        self.run_file(file)?;
        let init_result = self
            .call_bounded(|lua| lua.globals().get::<Function>("Init")?.call::<Value>(()))
            .map_err(|e| {
                e.with_context(format_args!("Init {}", file.display()), ErrorKind::Load)
            })?;
        let declared = declaration::take_declarations(&self.lua, init_result, file)?;
        declared.ok_or_else(|| {
            let message = format!("{}: Init never called NewWebsiteModule", file.display());
            Error::new(ErrorKind::Load, message)
        })
    }

    /// Loads a module file and runs its top-level code.
    fn run_file(&self, file: &Path) -> Result<()> {
        let context = format!("load {}", file.display());
        let src = std::fs::read(file)
            .map_err(|e| Error::new(ErrorKind::Load, format!("{context}: {e}")))?;
        self.call_bounded(|lua| sandbox::load_text(lua, file, &src)?.call::<()>(()))
            .map_err(|e| e.with_context(context, ErrorKind::Load))
    }

    /// Runs one call into the module under the CPU budget. Once the scrape is
    /// cancelled, it runs no calls at all.
    ///
    /// A call that is cancelled partway counts as a cancellation even if the
    /// module caught the termination and returned normally, so its result is
    /// never treated as real.
    pub(crate) fn call_bounded<R>(&self, call: impl FnOnce(&Lua) -> mlua::Result<R>) -> Result<R> {
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
