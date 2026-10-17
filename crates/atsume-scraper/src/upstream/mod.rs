//! The globals FMD2 gives modules, each in a file named after it.
//!
//! An object here keeps no state of its own. What it reads and writes is a
//! plain Rust value kept in the runtime, so the host reads a handler's results
//! straight back; see [`crate::runtime::bind`].

pub(crate) mod declaration;
pub(crate) mod directory;
pub(crate) mod document;
pub(crate) mod globals;
pub(crate) mod http;
pub(crate) mod libs;
pub(crate) mod mangainfo;
pub(crate) mod module;
pub(crate) mod string_list;
pub(crate) mod task;
pub(crate) mod txquery;

use std::path::Path;

use mlua::Lua;

use crate::http::HttpSession;

/// Stores the state behind the globals and sets the globals, before a module
/// file runs.
///
/// This is every value the objects here read and write, each kept once in the
/// runtime. Two are kept elsewhere: the sandbox keeps the file `io` may write,
/// and a session keeps the declared website behind `MODULE` once it knows it.
/// `lua_dir` is the checkout's `lua` directory, which `fmd.env` reports.
pub(crate) fn install(lua: &Lua, lua_dir: &Path, http: HttpSession) -> mlua::Result<()> {
    // `NewWebsiteModule`, until the host reads the declarations back.
    lua.set_app_data(declaration::Declarations::default());
    // The global `Storage`, and `MODULE.Storage`.
    lua.set_app_data(declaration::Storage::default());
    // `MODULE.Account`.
    lua.set_app_data(module::Account::default());
    // `HTTP`, and the lists and document it hands out.
    lua.set_app_data(http);
    // `MANGAINFO`.
    lua.set_app_data(mangainfo::MangaInfo::default());
    // `TASK`.
    lua.set_app_data(task::Task::default());
    // `LINKS`, `NAMES` and `UPDATELIST`, and where progress messages go.
    lua.set_app_data(directory::Directory::default());
    lua.set_app_data(directory::StatusSink::default());

    globals::set_constants(lua)?;
    libs::preload(lua, lua_dir)?;
    lua.globals().set("HTTP", http::HttpObject)?;
    declaration::register(lua)?;
    mangainfo::register(lua)?;
    task::register(lua)?;
    directory::register(lua)?;
    txquery::register(lua)
}
