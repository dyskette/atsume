//! `SeparateLeft`, which returns the text before the first separator, such as
//! the key of a `key=value` pair.

use mlua::{Lua, Value};

use crate::byte_search::split_once;
use crate::runtime::convert::bytes_arg;

/// Sets the global `SeparateLeft(text, separator)`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let separate_left = lua.create_function(|lua, (text, separator): (Value, Value)| {
        let text = bytes_arg(lua, &text, 1)?;
        let separator = bytes_arg(lua, &separator, 2)?;
        lua.create_string(text_before_separator(&text, &separator))
    })?;
    lua.globals().set("SeparateLeft", separate_left)
}

/// Returns the text before the first `separator`, or the whole text when the
/// separator does not occur.
fn text_before_separator<'a>(text: &'a [u8], separator: &[u8]) -> &'a [u8] {
    split_once(text, separator).map_or(text, |(before, _)| before)
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(super::register, source)
    }

    #[test]
    fn separate_left_returns_the_text_before_the_separator() {
        assert_eq!(
            eval("return SeparateLeft('key=value', '=') .. '|' .. SeparateLeft('plain', '=')")
                .unwrap(),
            "key|plain"
        );
        assert_eq!(eval("return SeparateLeft('a=b=c', '=')").unwrap(), "a");
        assert_eq!(eval("return SeparateLeft(12.5, '.')").unwrap(), "12");
    }

    #[test]
    fn separate_left_needs_both_arguments() {
        assert!(eval("return SeparateLeft('key=value')").is_err());
        assert!(eval("return SeparateLeft({}, '=')").is_err());
    }
}
