//! The `fmd.*` libraries that modules load with `require`.

use std::path::Path;

use mlua::{Lua, Table};

/// Registers each library in `package.preload`, so that `require` builds it on
/// first use.
pub(crate) fn preload(lua: &Lua, lua_dir: &Path) -> mlua::Result<()> {
    let preload: Table = lua.globals().get::<Table>("package")?.get("preload")?;
    // Upstream ends the directory with a separator, and modules append file
    // names to it directly.
    let lua_directory = format!("{}{}", lua_dir.display(), std::path::MAIN_SEPARATOR);
    let env = lua.create_function(move |lua, ()| env(lua, &lua_directory))?;
    preload.set("fmd.env", env)
}

/// Builds `fmd.env`, which describes the installation that modules run in.
fn env(lua: &Lua, lua_directory: &str) -> mlua::Result<Table> {
    let env = lua.create_table()?;
    // Modules branch on this to pick a language-specific page or parser.
    env.set("SelectedLanguage", "en")?;
    env.set("LuaDirectory", lua_directory)?;
    Ok(env)
}
