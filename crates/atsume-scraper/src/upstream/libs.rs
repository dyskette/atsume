//! The `fmd.*` libraries that modules load with `require`.
//!
//! A library function's doc comment gives, in parentheses, the name that
//! modules call it by.

mod banner_removal;
mod crypto;
mod encoding;
mod file_stem;
mod gzip;
mod image_codec;
mod image_puzzle;
mod javascript;
mod logging;

use std::path::Path;

use mlua::{Function, Lua, Table, Value};

use crate::runtime::convert::bytes_arg;
use crate::runtime::sandbox::checkout_root;

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
        lua.create_function(|lua, ()| file_stem::build(lua))?,
    )?;
    preload.set("fmd.gzip", lua.create_function(|lua, ()| gzip::build(lua))?)?;
    preload.set(
        "fmd.imagepuzzle",
        lua.create_function(|lua, ()| image_puzzle::build(lua))?,
    )?;
    let owned_lua_dir = lua_dir.to_path_buf();
    preload.set(
        "fmd.duktape",
        lua.create_function(move |lua, ()| javascript::build(lua, &owned_lua_dir))?,
    )?;
    let root = checkout_root(lua_dir)?;
    preload.set(
        "fmd.mangafoxwatermark",
        lua.create_function(move |lua, ()| banner_removal::build(lua, root.clone()))?,
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

/// A library function that takes the bytes of one string and returns new
/// bytes.
type BytesTransform = fn(&[u8]) -> Vec<u8>;

/// Wraps `transform` as a Lua function that takes one string, or a number
/// read as its text, and returns a string.
fn bytes_function(lua: &Lua, transform: BytesTransform) -> mlua::Result<Function> {
    lua.create_function(move |lua, value: Value| {
        let input = bytes_arg(lua, &value, 1)?;
        lua.create_string(transform(&input))
    })
}

#[cfg(test)]
mod tests {
    use crate::testing::open_module;

    #[test]
    fn modules_load_each_library_by_name() {
        let session = open_module("NewWebsiteModule()", "");
        for name in [
            "fmd.env",
            "fmd.crypto",
            "fmd.logger",
            "fmd.fileutil",
            "fmd.gzip",
            "fmd.imagepuzzle",
            "fmd.mangafoxwatermark",
            "fmd.duktape",
        ] {
            assert_eq!(
                session.eval(&format!("type(require '{name}')")),
                "table",
                "{name}"
            );
        }
    }
}
