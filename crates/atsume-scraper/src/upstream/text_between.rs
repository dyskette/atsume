//! `GetBetween`, which cuts a value out of a page or a script by the text
//! around it.

use mlua::{Lua, Value};

use crate::byte_search::split_once;
use crate::runtime::convert::bytes_arg;

/// Sets the global `GetBetween(left, right, text)`.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let get_between = lua.create_function(|lua, (left, right, text): (Value, Value, Value)| {
        let left = bytes_arg(lua, &left, 1)?;
        let right = bytes_arg(lua, &right, 2)?;
        let text = bytes_arg(lua, &text, 3)?;
        lua.create_string(text_between(&left, &right, &text))
    })?;
    lua.globals().set("GetBetween", get_between)
}

/// Returns the text after the first `left` and before the first `right` that
/// follows it. The result is empty when `left` does not occur, or when no
/// `right` follows it.
fn text_between<'a>(left: &[u8], right: &[u8], text: &'a [u8]) -> &'a [u8] {
    let Some((_, after_left)) = split_once(text, left) else {
        return &[];
    };
    split_once(after_left, right).map_or(&[], |(between, _)| between)
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(super::register, source)
    }

    #[test]
    fn get_between_returns_the_text_between_left_and_right() {
        assert_eq!(
            eval("return GetBetween('a[', ']', 'xa[mid]y') .. '|' .. GetBetween('<', '>', 'none')")
                .unwrap(),
            "mid|"
        );
        assert_eq!(
            eval("return GetBetween('=', ';', 'a=1;b=2;')").unwrap(),
            "1"
        );
        assert_eq!(
            eval("return '[' .. GetBetween('=', ';', 'a=1') .. ']'").unwrap(),
            "[]"
        );
        assert_eq!(eval("return GetBetween('', ';', 'a;b')").unwrap(), "a");
    }

    #[test]
    fn get_between_keeps_bytes_that_are_not_utf_8() {
        assert_eq!(
            eval("return GetBetween('[', ']', '[\\255\\254]') == '\\255\\254'").unwrap(),
            "true"
        );
    }

    #[test]
    fn get_between_reads_numbers_and_refuses_other_values() {
        assert_eq!(eval("return GetBetween(1, 3, 12345)").unwrap(), "2");
        assert!(eval("return GetBetween({}, ']', 'x')").is_err());
        assert!(eval("return GetBetween('[', ']')").is_err());
    }
}
