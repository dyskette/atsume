//! The globals through which the host and a module exchange values.
//!
//! FMD2's contract passes a handler's input and some of its output through
//! globals rather than arguments: `URL` in, `PAGENUMBER` out, and so on. This
//! module is the one place that reads and writes them.

use mlua::{Lua, Value};

use crate::convert::to_int;
use crate::site::AccountStatus;

/// The code a handler returns when a request it needed failed.
pub(crate) const NET_PROBLEM: i64 = 1;

/// The code `OnGetInfo` returns when the page holds no series.
pub(crate) const INFORMATION_NOT_FOUND: i64 = 2;

/// A handler's return codes, which modules read as globals.
const RETURN_CODES: [(&str, i64); 3] = [
    ("no_error", 0),
    ("net_problem", NET_PROBLEM),
    ("information_not_found", INFORMATION_NOT_FOUND),
];

/// Sets the constants every module reads: return codes and account states.
pub(crate) fn set_constants(lua: &Lua) -> mlua::Result<()> {
    let globals = lua.globals();
    for (name, code) in RETURN_CODES {
        globals.set(name, code)?;
    }
    for status in AccountStatus::ALL {
        globals.set(format!("as{status:?}"), status.code())?;
    }
    set_page_count(lua, 1)
}

/// Sets `URL`, the address a handler works on.
pub(crate) fn set_url(lua: &Lua, url: &str) -> mlua::Result<()> {
    lua.globals().set("URL", url)
}

/// Sets `FILENAME`, the saved page `OnAfterImageSaved` edits.
pub(crate) fn set_filename(lua: &Lua, path: &std::path::Path) -> mlua::Result<()> {
    lua.globals().set(
        "FILENAME",
        lua.create_string(path.to_string_lossy().as_bytes())?,
    )
}

/// Sets `PAGENUMBER`, which `OnGetDirectoryPageNumber` overwrites with the
/// directory's page count.
pub(crate) fn set_page_count(lua: &Lua, pages: i64) -> mlua::Result<()> {
    lua.globals().set("PAGENUMBER", pages)
}

/// Reads `PAGENUMBER` back as a whole number.
pub(crate) fn page_count(lua: &Lua) -> mlua::Result<i64> {
    Ok(to_int(lua, &lua.globals().get::<Value>("PAGENUMBER")?))
}
