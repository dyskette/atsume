//! Reads Lua values the way upstream's host reads them.

use mlua::{Lua, Value};

/// Reads a string or a number as the text that Lua would concatenate. Any
/// other value reads as `None`.
pub(crate) fn lua_text(lua: &Lua, value: &Value) -> Option<String> {
    let (Value::String(_) | Value::Integer(_) | Value::Number(_)) = value else {
        return None;
    };
    let text = lua.coerce_string(value.clone()).ok().flatten()?;
    Some(text.to_string_lossy())
}

/// Trims text as Pascal's `Trim` does. It removes spaces and control
/// characters, but not other Unicode whitespace such as a no-break space.
pub(crate) fn pascal_trim(text: &str) -> &str {
    text.trim_matches(|c: char| c <= ' ')
}

/// Trims the bytes of a Lua string as Pascal's `Trim` does. It removes each
/// byte up to and including a space from both ends.
pub(crate) fn pascal_trim_bytes(text: &[u8]) -> &[u8] {
    let kept = |byte: &u8| *byte > b' ';
    let start = text.iter().position(kept).unwrap_or(text.len());
    let end = text.iter().rposition(kept).map_or(start, |last| last + 1);
    &text[start..end]
}

/// Reports a value's Lua truthiness, in which everything except `nil` and
/// `false` is true.
pub(crate) fn truthy(value: &Value) -> bool {
    !matches!(value, Value::Nil | Value::Boolean(false))
}

/// Reads a number, or a string that holds one, truncated toward zero. Any
/// other value reads as 0.
pub(crate) fn lua_int(lua: &Lua, value: &Value) -> i64 {
    if let Value::Integer(n) = value {
        return *n;
    }
    let Ok(Some(n)) = lua.coerce_number(value.clone()) else {
        return 0;
    };
    #[expect(
        clippy::cast_possible_truncation,
        reason = "truncating toward zero is the conversion modules expect"
    )]
    let truncated = n.trunc() as i64;
    truncated
}

/// Reads argument number `position` as a string, accepting a number as Lua
/// does.
pub(crate) fn text_arg(lua: &Lua, value: &Value, position: usize) -> mlua::Result<String> {
    lua_text(lua, value).ok_or_else(|| bad_argument(position, "string", value))
}

/// Reads argument number `position` as the bytes of a string, accepting a
/// number as Lua does.
///
/// A Lua string may hold a page in a legacy encoding or binary data, so its
/// bytes are kept as they are instead of being read as UTF-8.
pub(crate) fn bytes_arg(lua: &Lua, value: &Value, position: usize) -> mlua::Result<Vec<u8>> {
    if let Value::String(text) = value {
        return Ok(text.as_bytes().to_vec());
    }
    text_arg(lua, value, position).map(String::into_bytes)
}

/// Reads argument number `position` as an integer. A float or a numeric
/// string is accepted and truncated.
pub(crate) fn int_arg(lua: &Lua, value: &Value, position: usize) -> mlua::Result<i64> {
    match lua.coerce_number(value.clone()) {
        Ok(Some(_)) => Ok(lua_int(lua, value)),
        _ => Err(bad_argument(position, "number", value)),
    }
}

/// Builds Lua's error for an argument of the wrong type.
pub(crate) fn bad_argument(position: usize, expected: &str, got: &Value) -> mlua::Error {
    mlua::Error::runtime(format!(
        "bad argument #{position} ({expected} expected, got {})",
        got.type_name()
    ))
}
