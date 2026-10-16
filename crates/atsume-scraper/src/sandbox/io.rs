//! An `io` library that reads within the checkout and writes one file.
//!
//! Only `io.open` and `io.lines` exist, since those are all the modules use.
//! Reads stay inside the checkout, except for the [`WritableFile`], which is
//! also the only file that may be written. Any other write fails with `nil`
//! and a message, as on a read-only filesystem.

use std::path::{Path, PathBuf};

use mlua::{
    AnyUserData, Function, Lua, MultiValue, Table, UserData, UserDataMethods, Value, Variadic,
};

use super::{WritableFile, normalize, read_in_root};
use crate::bind::state;
use crate::convert::text_of;

pub(super) fn library(lua: &Lua, root: PathBuf) -> mlua::Result<Table> {
    let io = lua.create_table()?;
    let opener = root.clone();
    io.set(
        "open",
        lua.create_function(move |lua, (name, mode): (String, Option<String>)| {
            let files = Files::now(lua, &opener)?;
            let file = files.open(&name, mode.as_deref().unwrap_or("r"));
            lua_result(lua, file.map(|file| lua.create_userdata(file)))
        })?,
    )?;
    io.set(
        "lines",
        lua.create_function(move |lua, name: String| {
            let data = Files::now(lua, &root)?
                .read(&name)
                .map_err(mlua::Error::runtime)?;
            lines_iterator(lua, lua.create_userdata(LuaFile::reading(data))?)
        })?,
    )?;
    Ok(io)
}

/// Converts a result into Lua's convention: the value, or `nil` and a message.
fn lua_result<T: mlua::IntoLua>(
    lua: &Lua,
    result: Result<mlua::Result<T>, String>,
) -> mlua::Result<(Value, Value)> {
    match result {
        Ok(value) => Ok((value?.into_lua(lua)?, Value::Nil)),
        Err(msg) => Ok((Value::Nil, Value::String(lua.create_string(msg)?))),
    }
}

/// What a module may open: anything inside `root` for reading, and the
/// writable file for reading or writing.
struct Files<'a> {
    root: &'a Path,
    writable: Option<PathBuf>,
}

impl<'a> Files<'a> {
    /// Returns what may be opened at this moment.
    fn now(lua: &Lua, root: &'a Path) -> mlua::Result<Self> {
        let writable = state::<WritableFile>(lua)?.0.clone();
        Ok(Self { root, writable })
    }

    fn open(&self, name: &str, mode: &str) -> Result<LuaFile, String> {
        if mode.contains(['w', 'a', '+']) {
            self.open_for_writing(name, mode)
        } else {
            self.read(name).map(LuaFile::reading)
        }
    }

    fn read(&self, name: &str) -> Result<Vec<u8>, String> {
        if self.is_writable(name) {
            return std::fs::read(name).map_err(|e| format!("{name}: {e}"));
        }
        read_in_root(self.root, name)
    }

    fn is_writable(&self, name: &str) -> bool {
        self.writable
            .as_deref()
            .is_some_and(|target| normalize(Path::new(name)) == normalize(target))
    }

    /// Opens the writable file: `w` replaces it and `a` appends to it. Modes
    /// that both read and write are refused, since no module needs one.
    fn open_for_writing(&self, name: &str, mode: &str) -> Result<LuaFile, String> {
        if !self.is_writable(name) || mode.contains('+') {
            return Err(format!("{name}: read-only file system"));
        }
        let path = normalize(Path::new(name));
        let data = if mode.starts_with('a') {
            read_existing(&path)
        } else {
            Ok(Vec::new())
        };
        let data = data.map_err(|e| format!("{name}: {e}"))?;
        Ok(LuaFile {
            data,
            pos: 0,
            closed: false,
            path: Some(path),
        })
    }
}

/// Reads a file to append to, treating a missing one as empty.
fn read_existing(path: &Path) -> std::io::Result<Vec<u8>> {
    match std::fs::read(path) {
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => Ok(Vec::new()),
        result => result,
    }
}

/// A file opened by `io.open`, held in memory.
///
/// A file opened for reading is read whole up front, so no descriptor stays
/// open between handler calls. A file opened for writing is saved only on
/// `close`, so a handler that never closes it leaves the file unchanged.
struct LuaFile {
    data: Vec<u8>,
    pos: usize,
    closed: bool,
    /// Where `close` saves the data, for a file opened for writing.
    path: Option<PathBuf>,
}

impl LuaFile {
    fn reading(data: Vec<u8>) -> Self {
        Self {
            data,
            pos: 0,
            closed: false,
            path: None,
        }
    }

    fn check_open(&self) -> mlua::Result<()> {
        if self.closed {
            return Err(mlua::Error::runtime("attempt to use a closed file"));
        }
        Ok(())
    }

    /// Reads one `file:read` format, returning `None` at the end of the file.
    ///
    /// Accepts `a`, `l` and `L`, with or without Lua 5.1's `*` prefix.
    fn read_format(&mut self, format: &str) -> mlua::Result<Option<Vec<u8>>> {
        let read = match format.trim_start_matches('*') {
            "a" | "all" => Some(self.read_rest()),
            "l" | "line" => self.read_line(false).map(<[u8]>::to_vec),
            "L" => self.read_line(true).map(<[u8]>::to_vec),
            _ => return Err(mlua::Error::runtime(format!("invalid format {format:?}"))),
        };
        Ok(read)
    }

    fn read_rest(&mut self) -> Vec<u8> {
        let rest = self.data[self.pos..].to_vec();
        self.pos = self.data.len();
        rest
    }

    /// Returns the next line, or `None` at the end of the file.
    ///
    /// Without the newline, a trailing `\r` goes too: FMD2 runs on Windows, so
    /// its files may have CRLF line endings.
    fn read_line(&mut self, keep_newline: bool) -> Option<&[u8]> {
        if self.pos >= self.data.len() {
            return None;
        }
        let rest = &self.data[self.pos..];
        let Some(i) = rest.iter().position(|&b| b == b'\n') else {
            self.pos = self.data.len();
            return Some(rest);
        };
        self.pos += i + 1;
        if keep_newline {
            return Some(&rest[..=i]);
        }
        Some(rest[..i].strip_suffix(b"\r").unwrap_or(&rest[..i]))
    }

    /// Saves a file opened for writing, returning the error message on failure.
    fn save(&self) -> Result<(), String> {
        let Some(path) = &self.path else {
            return Ok(());
        };
        std::fs::write(path, &self.data).map_err(|e| format!("{}: {e}", path.display()))
    }
}

impl UserData for LuaFile {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_method_mut("read", file_read);
        methods.add_function("lines", |lua, file: AnyUserData| {
            file.borrow::<LuaFile>()?.check_open()?;
            lines_iterator(lua, file)
        });
        methods.add_function("write", file_write);
        methods.add_method_mut("close", |lua, file, ()| {
            file.check_open()?;
            file.closed = true;
            lua_result(lua, file.save().map(|()| Ok(true)))
        });
    }
}

/// `file:read(...)`: one value per format, stopping at the first that finds
/// nothing, as in Lua.
fn file_read(lua: &Lua, file: &mut LuaFile, formats: Variadic<Value>) -> mlua::Result<MultiValue> {
    file.check_open()?;
    let formats = if formats.is_empty() {
        vec![Value::Nil]
    } else {
        formats.into_iter().collect()
    };
    let mut out = MultiValue::new();
    for format in formats {
        let format = match format {
            Value::Nil => "l".to_owned(),
            Value::String(s) => s.to_str()?.to_owned(),
            _ => return Err(mlua::Error::runtime("invalid format")),
        };
        let Some(read) = file.read_format(&format)? else {
            out.push_back(Value::Nil);
            break;
        };
        out.push_back(Value::String(lua.create_string(read)?));
    }
    Ok(out)
}

/// `file:write(...)`: appends each string or number and returns the file, so
/// calls can chain, as in Lua.
fn file_write(
    lua: &Lua,
    (handle, args): (AnyUserData, Variadic<Value>),
) -> mlua::Result<(Value, Value)> {
    let mut file = handle.borrow_mut::<LuaFile>()?;
    file.check_open()?;
    if file.path.is_none() {
        return lua_result::<Value>(lua, Err("file not opened for writing".to_owned()));
    }
    for (i, arg) in args.iter().enumerate() {
        let text = text_of(lua, arg).ok_or_else(|| {
            mlua::Error::runtime(format!(
                "bad argument #{} to 'write' (string expected, got {})",
                i + 1,
                arg.type_name()
            ))
        })?;
        file.data.extend_from_slice(text.as_bytes());
    }
    drop(file);
    Ok((Value::UserData(handle), Value::Nil))
}

/// Returns a function that yields the file's remaining lines, then `nil`.
fn lines_iterator(lua: &Lua, file: AnyUserData) -> mlua::Result<Function> {
    lua.create_function(move |lua, ()| {
        let mut file = file.borrow_mut::<LuaFile>()?;
        file.read_line(false)
            .map(|line| lua.create_string(line))
            .transpose()
            .map(|line| line.map_or(Value::Nil, Value::String))
    })
}
