//! The globals that FMD2 gives modules. Each file defines one global, or a few
//! related ones, and is named after the data they hold or the work they do.
//!
//! An object here keeps no state of its own. Everything it reads and writes is
//! a plain Rust value kept in the runtime, so the host can read a handler's
//! results straight back. [`crate::runtime::bind`] explains how.
//!
//! The Rust names in this module describe what each value holds, so some of
//! them differ from the names FMD2 gives modules. Each FMD2 name still appears
//! in two places: in the string that binds the value to Lua, and in the doc
//! comment of the Rust item behind it. Searching for `TASK.PageNumber` finds
//! both.

pub(crate) mod chapter_pages;
pub(crate) mod declaration;
pub(crate) mod directory;
pub(crate) mod document;
pub(crate) mod globals;
pub(crate) mod http;
pub(crate) mod libs;
pub(crate) mod module;
pub(crate) mod series;
pub(crate) mod string_list;
pub(crate) mod text_after_separator;
pub(crate) mod text_before_separator;
pub(crate) mod text_between;
pub(crate) mod trim;
pub(crate) mod txquery;

use std::path::Path;

use mlua::Lua;

use crate::http::HttpSession;

/// Stores the state behind the globals, and then sets the globals, before a
/// module file runs.
///
/// The state is every value that the objects here read and write, each kept
/// once in the runtime. Two values are kept elsewhere. The sandbox keeps the
/// file that `io` may write, and a session keeps the declared website behind
/// `MODULE` once it knows that website. `lua_dir` is the checkout's `lua`
/// directory, which `fmd.env` reports.
pub(crate) fn install(lua: &Lua, lua_dir: &Path, http: HttpSession) -> mlua::Result<()> {
    // `NewWebsiteModule` adds declarations here until the host reads them back.
    lua.set_app_data(declaration::PendingDeclarations::default());
    // The global `Storage` and `MODULE.Storage` both read this one.
    lua.set_app_data(declaration::Storage::default());
    // `MODULE.Account` reads this.
    lua.set_app_data(module::AccountRecord::default());
    // `HTTP` reads this, as do the lists and the document that it hands out.
    lua.set_app_data(http);
    // `MANGAINFO` reads this.
    lua.set_app_data(series::SeriesRecord::default());
    // `TASK` reads this.
    lua.set_app_data(chapter_pages::ChapterPagesRecord::default());
    // `LINKS`, `NAMES` and `UPDATELIST` read this, and the next value says
    // where progress messages go.
    lua.set_app_data(directory::DirectoryPageRecord::default());
    lua.set_app_data(directory::StatusSink::default());

    globals::set_constants(lua)?;
    libs::preload(lua, lua_dir)?;
    lua.globals().set("HTTP", http::HttpObject)?;
    declaration::register(lua)?;
    series::register(lua)?;
    chapter_pages::register(lua)?;
    directory::register(lua)?;
    txquery::register(lua)?;
    trim::register(lua)?;
    text_between::register(lua)?;
    text_before_separator::register(lua)?;
    text_after_separator::register(lua)
}
