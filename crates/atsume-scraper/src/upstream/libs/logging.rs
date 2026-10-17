//! `fmd.logger`, through which modules report what they are doing.

use mlua::{Function, Lua, Table, Value};

use crate::runtime::convert::bytes_arg;

/// Builds the `fmd.logger` table.
///
/// `Send` logs at debug level, because modules use it to trace their own
/// progress. `SendError` logs a warning, because modules use it when a page
/// was not what they expected.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let logger = lua.create_table()?;
    logger.set(
        "Send",
        log_function(lua, |message| tracing::debug!(message, "lua module"))?,
    )?;
    logger.set(
        "SendError",
        log_function(lua, |message| tracing::warn!(message, "lua module"))?,
    )?;
    Ok(logger)
}

/// Wraps `log` as a Lua function that takes one message.
fn log_function(lua: &Lua, log: fn(&str)) -> mlua::Result<Function> {
    lua.create_function(move |lua, message: Value| {
        let message = bytes_arg(lua, &message, 1)?;
        log(&String::from_utf8_lossy(&message));
        Ok(())
    })
}

#[cfg(test)]
mod tests {
    use crate::testing::eval_registered;

    fn eval(source: &str) -> mlua::Result<String> {
        eval_registered(
            |lua| lua.globals().set("logger", super::build(lua)?),
            source,
        )
    }

    #[test]
    fn modules_can_log_progress_and_errors() {
        let logged = eval("logger.Send('reading page 2'); logger.SendError(404); return 'logged'");
        assert_eq!(logged.unwrap(), "logged");
    }

    #[test]
    fn a_log_call_needs_a_message() {
        assert!(eval("logger.Send()").is_err());
        assert!(eval("logger.SendError({})").is_err());
    }
}
