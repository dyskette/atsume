//! `SeparateRight`, which returns the text after the first separator, such as
//! the value of a `key=value` pair.

use mlua::{Lua, Value};

use crate::byte_search::split_once;
use crate::runtime::convert::bytes_arg;

/// Sets the global `SeparateRight(text, separator)`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let separate_right = lua.create_function(|lua, (text, separator): (Value, Value)| {
        let text = bytes_arg(lua, &text, 1)?;
        let separator = bytes_arg(lua, &separator, 2)?;
        lua.create_string(text_after_separator(&text, &separator))
    })?;
    lua.globals().set("SeparateRight", separate_right)
}

/// Returns the text after the first `separator`, or the whole text when the
/// separator does not occur.
fn text_after_separator<'a>(text: &'a [u8], separator: &[u8]) -> &'a [u8] {
    split_once(text, separator).map_or(text, |(_, after)| after)
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(super::register, source)
    }

    #[test]
    fn separate_right_returns_the_text_after_the_separator() {
        assert_eq!(
            eval("return SeparateRight('key=value', '=') .. '|' .. SeparateRight('plain', '=')")
                .unwrap(),
            "value|plain"
        );
        assert_eq!(eval("return SeparateRight('a=b=c', '=')").unwrap(), "b=c");
        assert_eq!(eval("return SeparateRight(12.5, '.')").unwrap(), "5");
    }

    #[test]
    fn separate_right_needs_both_arguments() {
        assert!(eval("return SeparateRight('key=value')").is_err());
        assert!(eval("return SeparateRight({}, '=')").is_err());
    }
}
