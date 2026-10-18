//! Stand-ins for libraries that atsume does not provide, such as
//! `fmd.subprocess`. A stand-in still loads, so calling one of its functions
//! fails with an error that names the function.
//!
//! Without such a library, a module that calls one of its functions fails
//! with "attempt to call a nil value", which says nothing about what is
//! missing.

use mlua::{Lua, MetaMethod, Table, Value};

use crate::runtime::convert::lua_text;

/// Builds a stand-in for the library `library_name`, in which every field
/// reads as a function that raises an error.
///
/// The error is the library's name, the field's name and `reason`, as in
/// `fmd.subprocess.RunCommand requires external process execution`. `reason`
/// therefore completes a sentence whose subject is the function, so it starts
/// with a verb. A field whose name is neither text nor a number reads with an
/// empty name.
pub(super) fn build(
    lua: &Lua,
    library_name: &'static str,
    reason: &'static str,
) -> mlua::Result<Table> {
    let raising_function_for_field =
        lua.create_function(move |lua, (_, field): (Table, Value)| {
            let field = lua_text(lua, &field).unwrap_or_default();
            lua.create_function(move |_, ()| -> mlua::Result<()> {
                Err(mlua::Error::runtime(format!(
                    "{library_name}.{field} {reason}"
                )))
            })
        })?;
    let metatable = lua.create_table()?;
    metatable.set(MetaMethod::Index.name(), raising_function_for_field)?;
    let table = lua.create_table()?;
    table.set_metatable(Some(metatable))?;
    Ok(table)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::eval_registered;

    /// Runs `source` with an unavailable library in the global `subprocess`.
    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(
            |lua| {
                let library = build(lua, "fmd.subprocess", "is not supported")?;
                lua.globals().set("subprocess", library)
            },
            source,
        )
    }

    #[test]
    fn every_field_is_a_function() {
        assert_eq!(
            eval("return type(subprocess.RunCommand)").unwrap(),
            "function"
        );
        assert_eq!(
            eval("return type(subprocess.anything_at_all)").unwrap(),
            "function"
        );
    }

    #[test]
    fn calling_a_field_names_the_library_the_field_and_the_reason() {
        let error = eval("subprocess.RunCommand('ls', '-l')").unwrap_err();
        assert!(
            error
                .to_string()
                .contains("fmd.subprocess.RunCommand is not supported"),
            "{error}"
        );
        let numbered = eval("subprocess[3]()").unwrap_err();
        assert!(
            numbered
                .to_string()
                .contains("fmd.subprocess.3 is not supported"),
            "{numbered}"
        );
    }
}
