//! The globals through which the host and a module exchange values.
//!
//! FMD2's contract passes a handler's input, and some of its output, through
//! globals instead of arguments. For example, `URL` carries input and
//! `PAGENUMBER` carries output. This module is the only place that reads and
//! writes these globals.

use mlua::{Lua, Value};

use crate::model::AccountStatus;
use crate::runtime::convert::lua_int;

/// The code that a handler returns when a request it needed failed. Modules
/// refer to it as `net_problem`.
pub(crate) const RETURN_NETWORK_FAILURE: i64 = 1;

/// The code that `OnGetInfo` returns when the page holds no series. Modules
/// refer to it as `information_not_found`.
pub(crate) const RETURN_SERIES_NOT_FOUND: i64 = 2;

/// A handler's return codes, which modules read from globals of these names.
const RETURN_CODES: [(&str, i64); 3] = [
    ("no_error", 0),
    ("net_problem", RETURN_NETWORK_FAILURE),
    ("information_not_found", RETURN_SERIES_NOT_FOUND),
];

/// Sets the constants that every module reads, which are the return codes and
/// the account states.
pub(crate) fn set_constants(lua: &Lua) -> mlua::Result<()> {
    let globals = lua.globals();
    for (name, code) in RETURN_CODES {
        globals.set(name, code)?;
    }
    for status in AccountStatus::ALL {
        globals.set(format!("as{status:?}"), status.code())?;
    }
    set_directory_page_count(lua, 1)
}

/// Sets `URL`, the address that a handler works on.
pub(crate) fn set_url(lua: &Lua, url: &str) -> mlua::Result<()> {
    lua.globals().set("URL", url)
}

/// Sets `URL` to the number of a directory page, counting from 0.
/// `OnGetNameAndLink` reads `URL` as that number.
pub(crate) fn set_url_to_directory_page(lua: &Lua, page: u32) -> mlua::Result<()> {
    lua.globals().set("URL", page)
}

/// Sets `WORKID`, the index of the page that `OnGetImageURL` resolves,
/// counting from 0.
pub(crate) fn set_page_to_resolve(lua: &Lua, page: usize) -> mlua::Result<()> {
    lua.globals().set("WORKID", page)
}

/// Sets `FILENAME`, the saved page that `OnAfterImageSaved` edits.
pub(crate) fn set_filename(lua: &Lua, path: &std::path::Path) -> mlua::Result<()> {
    lua.globals().set(
        "FILENAME",
        lua.create_string(path.to_string_lossy().as_bytes())?,
    )
}

/// Sets `PAGENUMBER`, which `OnGetDirectoryPageNumber` overwrites with the
/// directory's page count.
pub(crate) fn set_directory_page_count(lua: &Lua, pages: i64) -> mlua::Result<()> {
    lua.globals().set("PAGENUMBER", pages)
}

/// Reads `PAGENUMBER` back as a whole number.
pub(crate) fn directory_page_count(lua: &Lua) -> mlua::Result<i64> {
    Ok(lua_int(lua, &lua.globals().get::<Value>("PAGENUMBER")?))
}
