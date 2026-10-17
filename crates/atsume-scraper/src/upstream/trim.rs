//! `Trim`, which removes the spaces and line breaks around a value that a
//! module read from a page.

use mlua::{Lua, Value};

use crate::runtime::convert::{bytes_arg, pascal_trim_bytes};

/// Sets the global `Trim`.
///
/// It trims as Pascal does, so a no-break space at either end stays, as it
/// does upstream.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let trim = lua.create_function(|lua, text: Value| {
        let text = bytes_arg(lua, &text, 1)?;
        lua.create_string(pascal_trim_bytes(&text))
    })?;
    lua.globals().set("Trim", trim)
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(super::register, source)
    }

    #[test]
    fn trim_removes_spaces_and_control_characters_at_both_ends() {
        assert_eq!(
            eval("return '[' .. Trim('  a b \\n') .. ']'").unwrap(),
            "[a b]"
        );
        assert_eq!(
            eval("return '[' .. Trim('\\t\\1x\\r') .. ']'").unwrap(),
            "[x]"
        );
        assert_eq!(eval("return '[' .. Trim('   ') .. ']'").unwrap(), "[]");
    }

    #[test]
    fn trim_keeps_a_no_break_space_and_any_other_byte() {
        assert_eq!(
            eval("return Trim(' \\u{a0}x ') == '\\u{a0}x'").unwrap(),
            "true"
        );
        assert_eq!(eval("return Trim(' \\255 ') == '\\255'").unwrap(), "true");
    }

    #[test]
    fn trim_reads_a_number_as_its_text_and_refuses_other_values() {
        assert_eq!(eval("return Trim(12)").unwrap(), "12");
        assert!(eval("return Trim({})").is_err());
        assert!(eval("return Trim()").is_err());
    }
}
