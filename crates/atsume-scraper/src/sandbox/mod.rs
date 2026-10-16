//! The Lua runtime a website module runs in.
//!
//! Modules are fetched from upstream at run time, so they are untrusted code.
//! The runtime holds only the parts of the standard library they use:
//!
//! - No `debug`, `dofile` or `loadfile`. `load` compiles source text only,
//!   because Lua does not verify bytecode and a crafted chunk can corrupt
//!   memory.
//! - `package` keeps `loaded` and `preload`. Everything that loads native code
//!   or searches the disk is removed, and `require` reads only from the
//!   checkout's `lua` directory.
//! - `os` keeps its clock functions. `os.remove` and `os.rename` exist but
//!   always fail, because `utils/nodejs.lua` uses `rename` as an existence
//!   check.
//! - `io` has only `open` and `lines`; see [`io`].
//! - Allocation is capped at [`MEMORY_LIMIT`].

mod io;

use std::path::{Component, Path, PathBuf};

use mlua::chunk::ChunkMode;
use mlua::{Function, Lua, LuaOptions, LuaString, MultiValue, StdLib, Table, Value, Variadic};

/// The most one module's runtime may allocate.
pub const MEMORY_LIMIT: usize = 512 * 1024 * 1024;

/// The one file a module may write, if any, kept in the runtime.
///
/// The runner sets it to the page `OnAfterImageSaved` is editing, which
/// upstream lets that hook rewrite in place, and clears it afterwards.
#[derive(Debug, Default)]
pub(crate) struct WritableFile(pub(crate) Option<PathBuf>);

/// Builds the sandboxed runtime for a module from the checkout at `lua_dir`.
///
/// `lua_dir` is the checkout's `lua` directory, where `require` looks. Relative
/// file paths resolve against its parent, which is FMD2's working directory.
///
/// # Errors
///
/// Fails if `lua_dir` cannot be made absolute or the runtime cannot be built.
pub(crate) fn new_runtime(lua_dir: &Path) -> mlua::Result<Lua> {
    let lua_dir = std::path::absolute(lua_dir).map_err(mlua::Error::external)?;
    let root = lua_dir.parent().unwrap_or(&lua_dir).to_path_buf();

    let libs = StdLib::COROUTINE
        | StdLib::TABLE
        | StdLib::STRING
        | StdLib::UTF8
        | StdLib::MATH
        | StdLib::OS
        | StdLib::PACKAGE;
    let lua = Lua::new_with(libs, LuaOptions::new())?;
    lua.set_memory_limit(MEMORY_LIMIT)?;

    let globals = lua.globals();
    globals.set("dofile", Value::Nil)?;
    globals.set("loadfile", Value::Nil)?;
    globals.set("load", text_only_load(&lua, globals.get("load")?)?)?;

    let package: Table = globals.get("package")?;
    for name in ["path", "cpath", "searchpath", "searchers", "loadlib"] {
        package.set(name, Value::Nil)?;
    }
    let require = Require { package, lua_dir };
    globals.set(
        "require",
        lua.create_function(move |lua, name: LuaString| require.call(lua, &name))?,
    )?;

    narrow_os(&lua, &globals.get("os")?)?;
    lua.set_app_data(WritableFile::default());
    globals.set("io", io::library(&lua, root)?)?;
    Ok(lua)
}

/// Wraps the base library's `load` so it compiles source text only.
fn text_only_load(lua: &Lua, load: Function) -> mlua::Result<Function> {
    lua.create_function(move |lua, args: Variadic<Value>| {
        let mut args = args.to_vec();
        // Pad to the mode argument without adding an environment: `load`
        // treats a passed `nil` environment differently from none.
        if args.len() < 3 {
            args.resize(3, Value::Nil);
        }
        args[2] = Value::String(lua.create_string("t")?);
        load.call::<MultiValue>(MultiValue::from_vec(args))
    })
}

/// The `require` modules call, confined to the checkout.
///
/// It looks in `package.loaded`, then `package.preload`, then for `name.lua`
/// or `name/init.lua` under `lua_dir`, with dots as directory separators.
struct Require {
    package: Table,
    lua_dir: PathBuf,
}

impl Require {
    fn call(&self, lua: &Lua, name: &LuaString) -> mlua::Result<(Value, Value)> {
        let name = name.to_str()?.to_owned();
        let loaded: Table = self.package.get("loaded")?;
        let cached: Value = loaded.get(name.as_str())?;
        if !cached.is_nil() {
            return Ok((cached, Value::Nil));
        }

        let (load_module, extra) = self.find_loader(lua, &name)?;
        let mut module: Value = load_module.call((name.as_str(), extra.clone()))?;
        // As in Lua, a loader that returns nothing may have set
        // package.loaded itself, and otherwise the module is recorded as true.
        if module.is_nil() {
            module = loaded.get(name.as_str())?;
        }
        if module.is_nil() {
            module = Value::Boolean(true);
        }
        loaded.set(name.as_str(), module.clone())?;
        Ok((module, extra))
    }

    /// Returns the loader for `name` and the value Lua passes it second: the
    /// file's path, or `:preload:`.
    fn find_loader(&self, lua: &Lua, name: &str) -> mlua::Result<(Function, Value)> {
        let preload: Table = self.package.get("preload")?;
        match preload.get::<Value>(name)? {
            Value::Function(loader) => Ok((loader, Value::String(lua.create_string(":preload:")?))),
            Value::Nil => self.find_file(lua, name),
            other => Err(mlua::Error::runtime(format!(
                "package.preload['{name}'] is a {}, not a function",
                other.type_name()
            ))),
        }
    }

    fn find_file(&self, lua: &Lua, name: &str) -> mlua::Result<(Function, Value)> {
        let rel = name.replace('.', "/");
        let mut tried = Vec::new();
        for candidate in [format!("{rel}.lua"), format!("{rel}/init.lua")] {
            let path = self.lua_dir.join(&candidate);
            let Ok(src) = read_in_root(&self.lua_dir, &candidate) else {
                tried.push(format!("\n\tno file '{}'", path.display()));
                continue;
            };
            let chunk = load_text(lua, &path, &src).map_err(|e| {
                mlua::Error::runtime(format!(
                    "error loading module '{name}' from file '{}':\n\t{e}",
                    path.display()
                ))
            })?;
            let path = lua.create_string(path.to_string_lossy().as_bytes())?;
            return Ok((chunk, Value::String(path)));
        }
        Err(mlua::Error::runtime(format!(
            "module '{name}' not found:\n\tno field package.preload['{name}']{}",
            tried.concat()
        )))
    }
}

/// Compiles a file's source as a text chunk.
///
/// Like Lua's own `luaL_loadfile`, it skips a UTF-8 byte-order mark and a
/// leading `#` line. Several upstream modules start with a byte-order mark.
pub(crate) fn load_text(lua: &Lua, path: &Path, src: &[u8]) -> mlua::Result<Function> {
    let mut src = src.strip_prefix(b"\xEF\xBB\xBF").unwrap_or(src);
    if src.first() == Some(&b'#') {
        // Keep the newline, so line numbers in errors stay right.
        let line_end = src.iter().position(|&b| b == b'\n').unwrap_or(src.len());
        src = &src[line_end..];
    }
    lua.load(src)
        .set_name(format!("@{}", path.display()))
        .set_mode(ChunkMode::Text)
        .into_function()
}

/// Removes the `os` functions that reach the process or the filesystem.
///
/// `remove` and `rename` stay, but return `nil` and a message, as a failed
/// file operation does in Lua.
fn narrow_os(lua: &Lua, os: &Table) -> mlua::Result<()> {
    for name in ["execute", "exit", "getenv", "setlocale", "tmpname"] {
        os.set(name, Value::Nil)?;
    }
    let refuse = lua.create_function(|_, (path, _): (Option<String>, Variadic<Value>)| {
        let path = path.unwrap_or_default();
        Ok((Value::Nil, format!("{path}: operation not permitted")))
    })?;
    os.set("remove", refuse.clone())?;
    os.set("rename", refuse)?;
    Ok(())
}

/// Reads `name`, resolved against `root` when relative, refusing any path
/// that leads outside `root`.
///
/// The path is checked lexically before the filesystem is touched, so a module
/// cannot probe which files exist outside the checkout. It is checked again
/// once resolved, which refuses a symlink that leads out. Errors read the way
/// Lua's `io` reports them.
pub(crate) fn read_in_root(root: &Path, name: &str) -> Result<Vec<u8>, String> {
    let denied = || format!("{name}: permission denied");
    let candidate = normalize(&root.join(name));
    if !candidate.starts_with(normalize(root)) {
        return Err(denied());
    }
    let resolved = match candidate.canonicalize() {
        Ok(path) => path,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => {
            return Err(format!("{name}: No such file or directory"));
        }
        Err(_) => return Err(denied()),
    };
    let root = root.canonicalize().map_err(|_| denied())?;
    if !resolved.starts_with(&root) || !resolved.is_file() {
        return Err(denied());
    }
    std::fs::read(&resolved).map_err(|e| format!("{name}: {e}"))
}

/// Resolves `.` and `..` in a path without touching the filesystem.
pub(crate) fn normalize(path: &Path) -> PathBuf {
    let mut out = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir if out.pop() => {}
            _ => out.push(component),
        }
    }
    out
}

#[cfg(test)]
mod tests;
