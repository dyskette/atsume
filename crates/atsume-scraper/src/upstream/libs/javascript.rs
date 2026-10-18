//! `fmd.duktape`, which runs JavaScript that modules lift from pages.
//!
//! Some sites encrypt their page lists and ship the code that decrypts them in
//! the page. FMD2 runs that code in the Duktape engine, and atsume runs it in
//! QuickJS under the same library name. A script comes from a scraped page, so
//! it is untrusted. It can reach nothing but `require`, and `docs/MODULES.md`
//! describes what that serves.

mod require;
mod script_runner;
#[cfg(test)]
mod tests;

use std::path::Path;

use mlua::{Lua, Table, Value};

use crate::runtime::convert::text_arg;

use script_runner::ScriptRunner;

/// Builds the `fmd.duktape` table. Scripts may `require` helpers from
/// `lua_dir`, the checkout's `lua` directory.
///
/// # Errors
///
/// Fails if `lua_dir` cannot be made absolute.
pub(super) fn build(lua: &Lua, lua_dir: &Path) -> mlua::Result<Table> {
    let lua_dir = std::path::absolute(lua_dir).map_err(mlua::Error::external)?;
    let runner = ScriptRunner::new(lua_dir);
    let library = lua.create_table()?;
    let exec = lua.create_function(move |lua, source: Value| {
        let source = text_arg(lua, &source, 1)?;
        runner
            .run(&source)
            .map_err(|error| mlua::Error::runtime(format!("fmd.duktape.ExecJS: {error}")))
    })?;
    library.set("ExecJS", exec)?;
    Ok(library)
}
