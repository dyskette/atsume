//! The `fmd.*` libraries that modules load with `require`.

mod crypto;
mod file_names;
mod logging;

use std::path::Path;

use mlua::{Function, Lua, Table, Value};

use crate::runtime::convert::bytes_arg;

/// Registers each library in `package.preload`, so that `require` builds it on
/// first use.
pub(crate) fn preload(lua: &Lua, lua_dir: &Path) -> mlua::Result<()> {
    let preload: Table = lua.globals().get::<Table>("package")?.get("preload")?;
    // Upstream ends the directory with a separator, and modules append file
    // names to it directly.
    let lua_directory = format!("{}{}", lua_dir.display(), std::path::MAIN_SEPARATOR);
    let env = lua.create_function(move |lua, ()| env(lua, &lua_directory))?;
    preload.set("fmd.env", env)?;
    preload.set(
        "fmd.crypto",
        lua.create_function(|lua, ()| crypto::build(lua))?,
    )?;
    preload.set(
        "fmd.logger",
        lua.create_function(|lua, ()| logging::build(lua))?,
    )?;
    preload.set(
        "fmd.fileutil",
        lua.create_function(|lua, ()| file_names::build(lua))?,
    )
}

/// Builds `fmd.env`, which describes the installation that modules run in.
fn env(lua: &Lua, lua_directory: &str) -> mlua::Result<Table> {
    let env = lua.create_table()?;
    // Modules branch on this to pick a language-specific page or parser.
    env.set("SelectedLanguage", "en")?;
    env.set("LuaDirectory", lua_directory)?;
    Ok(env)
}

/// A library function that makes new bytes out of the bytes of one string.
type BytesTransform = fn(&[u8]) -> Vec<u8>;

/// Wraps `transform` as a Lua function that takes one string, or a number
/// read as its text, and returns a string.
fn bytes_function(lua: &Lua, transform: BytesTransform) -> mlua::Result<Function> {
    lua.create_function(move |lua, value: Value| {
        let input = bytes_arg(lua, &value, 1)?;
        lua.create_string(transform(&input))
    })
}
