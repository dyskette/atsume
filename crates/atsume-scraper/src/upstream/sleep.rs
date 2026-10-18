//! `sleep`, which pauses a module between requests.
//!
//! Some sites hand out only a few images per request unless the module waits
//! between requests. Skipping the pause would therefore lose pages.

use std::time::Duration;

use mlua::{Lua, Value};

use crate::cancel::CancelToken;
use crate::runtime::convert::int_arg;

/// The longest pause a module may ask for. No site needs a longer wait between
/// requests, so a module that asks for more has gone wrong, and the cap keeps
/// it from stalling the scrape.
const LONGEST_PAUSE: Duration = Duration::from_secs(30);

/// Sets the global `sleep(milliseconds)`.
///
/// A pause of zero milliseconds or less returns at once. Cancelling the
/// scrape ends the pause, and the call then fails, because the module would
/// otherwise carry on as though it had waited.
pub(crate) fn register(lua: &Lua, cancel: CancelToken) -> mlua::Result<()> {
    let sleep = lua.create_function(move |lua, milliseconds: Value| {
        let milliseconds = int_arg(lua, &milliseconds, 1)?;
        let pause = pause_for(milliseconds);
        if pause.is_zero() {
            return Ok(());
        }
        cancel.sleep(pause).map_err(mlua::Error::external)
    })?;
    lua.globals().set("sleep", sleep)
}

/// Returns how long to pause when a module asks for `milliseconds`. A negative
/// value means no pause, and no pause is longer than [`LONGEST_PAUSE`].
fn pause_for(milliseconds: i64) -> Duration {
    let milliseconds = u64::try_from(milliseconds).unwrap_or(0);
    Duration::from_millis(milliseconds).min(LONGEST_PAUSE)
}

#[cfg(test)]
mod tests {
    use std::time::Instant;

    use super::*;
    use crate::testing::{eval_registered, open_module_with};
    use crate::{ErrorKind, Event, http::HttpConfig};

    fn eval(cancel: CancelToken, source: &str) -> mlua::Result<String> {
        eval_registered(|lua| register(lua, cancel), source)
    }

    #[test]
    fn sleep_waits_for_the_time_asked() {
        let start = Instant::now();
        let slept = eval(
            CancelToken::new(),
            "sleep(20); sleep(0); sleep(-5); return 'slept'",
        );
        assert_eq!(slept.unwrap(), "slept");
        assert!(start.elapsed() >= Duration::from_millis(20));
    }

    #[test]
    fn a_pause_is_never_negative_nor_longer_than_the_cap() {
        assert_eq!(pause_for(-5), Duration::ZERO);
        assert_eq!(pause_for(1500), Duration::from_millis(1500));
        assert_eq!(pause_for(3_600_000), LONGEST_PAUSE);
    }

    #[test]
    fn sleep_needs_a_number() {
        assert!(eval(CancelToken::new(), "sleep('soon')").is_err());
        assert!(eval(CancelToken::new(), "sleep()").is_err());
        assert_eq!(
            eval(CancelToken::new(), "sleep('1'); return 'ok'").unwrap(),
            "ok"
        );
    }

    const SLEEPY_INIT: &str =
        "local m = NewWebsiteModule(); m.Name = 'Sleepy'; m.OnGetPageNumber = 'GetPageNumber'";

    /// Modules call `sleep` from their handlers, so the global must exist when
    /// a session runs a handler.
    #[test]
    fn a_handler_can_sleep() {
        let rest = "function GetPageNumber() sleep(20); sleep(0); sleep(-5); return true end";
        let session = open_module_with(
            &HttpConfig::default(),
            CancelToken::new(),
            SLEEPY_INIT,
            rest,
        );
        let start = Instant::now();
        session.call_handler(Event::ListChapterPages).unwrap();
        assert!(start.elapsed() >= Duration::from_millis(20));
    }

    /// Cancelling must end a long pause at once, instead of letting the scrape
    /// wait until the pause is over.
    #[test]
    fn cancelling_ends_a_sleep_and_the_call() {
        let rest = "function GetPageNumber() sleep(30000); return true end";
        let cancel = CancelToken::new();
        let session = open_module_with(&HttpConfig::default(), cancel.clone(), SLEEPY_INIT, rest);
        let canceller = std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(30));
            cancel.cancel();
        });
        let start = Instant::now();
        let err = session.call_handler(Event::ListChapterPages).unwrap_err();
        canceller.join().unwrap();
        assert_eq!(err.kind(), ErrorKind::Cancelled, "{err}");
        assert!(start.elapsed() < Duration::from_secs(5));
    }
}
