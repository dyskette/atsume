//! Runs one script in a fresh QuickJS runtime and reads its result back as
//! text.

use std::cell::Cell;
use std::path::PathBuf;
use std::rc::Rc;
use std::time::{Duration, Instant};

use rquickjs::context::EvalOptions;
use rquickjs::function::Rest;
use rquickjs::{Coerced, Context, Ctx, Function, Object, Runtime, Value};

use super::require;
use crate::runtime::sandbox::MEMORY_LIMIT;

/// How long one script may run before it is stopped. A script comes from a
/// scraped page, so a loop that never ends must not hold a scrape forever.
const TIME_LIMIT: Duration = Duration::from_secs(15);

/// Why a script produced no result.
#[derive(Debug, thiserror::Error)]
pub(super) enum ScriptError {
    #[error("the script ran longer than {}s and was stopped", .0.as_secs_f64())]
    TimedOut(Duration),
    #[error("{0}")]
    Thrown(String),
    #[error("the JavaScript engine failed: {0}")]
    Engine(rquickjs::Error),
}

/// Runs the scripts of one module. Each script runs in its own runtime, so
/// no state passes from one script to the next.
#[derive(Debug)]
pub(super) struct ScriptRunner {
    /// The checkout's `lua` directory, the only place `require` reads from.
    lua_dir: PathBuf,
    /// How long one script may run.
    pub(super) time_limit: Duration,
}

impl ScriptRunner {
    /// Creates a runner whose scripts may `require` files from `lua_dir`.
    pub(super) fn new(lua_dir: PathBuf) -> Self {
        Self {
            lua_dir,
            time_limit: TIME_LIMIT,
        }
    }

    /// Runs `source` and returns its completion value as text (`ExecJS`).
    ///
    /// The completion value is the value of the last expression statement,
    /// which modules use to return a result. It reads as text the way
    /// upstream's `duk_safe_to_string` reads it, so `null` reads as `null` and
    /// an array reads as its elements joined by commas. The one difference is
    /// `undefined`, which upstream reads as the word `undefined` and atsume
    /// reads as an empty string, so a script that returns nothing gives no
    /// text.
    ///
    /// # Errors
    ///
    /// Fails when the script throws, when it runs longer than the time limit,
    /// or when the engine cannot start.
    pub(super) fn run(&self, source: &str) -> Result<String, ScriptError> {
        let runtime = Runtime::new().map_err(ScriptError::Engine)?;
        runtime.set_memory_limit(MEMORY_LIMIT);
        let timed_out = stop_after(&runtime, self.time_limit);
        let context = Context::full(&runtime).map_err(ScriptError::Engine)?;
        let result = context.with(|ctx| {
            install_globals(&ctx, &self.lua_dir)
                .and_then(|()| ctx.eval_with_options::<Value, _>(source, sloppy_eval_options()))
                .and_then(|value| completion_text(&value))
                .map_err(|error| script_error(&ctx, error))
        });
        if timed_out.get() {
            return Err(ScriptError::TimedOut(self.time_limit));
        }
        result
    }
}

/// Returns the options for evaluating a script that a page ships.
///
/// Page scripts are written for sloppy mode, which assigns undeclared
/// variables to globals, so strict mode, the engine's default, is turned off.
pub(super) fn sloppy_eval_options() -> EvalOptions {
    let mut options = EvalOptions::default();
    options.strict = false;
    options
}

/// Makes `runtime` stop any script once `limit` has passed. Returns a flag
/// that is set when a script is stopped for that reason.
fn stop_after(runtime: &Runtime, limit: Duration) -> Rc<Cell<bool>> {
    let deadline = Instant::now() + limit;
    let timed_out = Rc::new(Cell::new(false));
    let flag = Rc::clone(&timed_out);
    runtime.set_interrupt_handler(Some(Box::new(move || {
        let expired = Instant::now() >= deadline;
        flag.set(expired);
        expired
    })));
    timed_out
}

/// Reads a script's completion value as text, as upstream's
/// `duk_safe_to_string` does, except that `undefined` reads as `""`.
fn completion_text(value: &Value<'_>) -> rquickjs::Result<String> {
    if value.is_undefined() {
        return Ok(String::new());
    }
    value.get::<Coerced<String>>().map(|text| text.0)
}

/// Turns an engine error into a [`ScriptError`], reading the thrown value
/// when the script threw one.
fn script_error(ctx: &Ctx<'_>, error: rquickjs::Error) -> ScriptError {
    if !error.is_exception() {
        return ScriptError::Engine(error);
    }
    let thrown = ctx.catch();
    let text = thrown.get::<Coerced<String>>().map_or_else(
        |_| "the script threw a value that has no text".to_owned(),
        |text| text.0,
    );
    ScriptError::Thrown(text)
}

/// Adds the globals that page scripts expect: `print`, `console` and
/// `require`.
///
/// Upstream defines `print` and no `console`. Some helpers call
/// `console.warn`, which would otherwise end the script over a warning, so
/// `console` is defined too. Both write to the debug log.
fn install_globals(ctx: &Ctx<'_>, lua_dir: &std::path::Path) -> rquickjs::Result<()> {
    let globals = ctx.globals();
    globals.set("print", log_function(ctx, "print")?)?;
    let console = Object::new(ctx.clone())?;
    for level in ["log", "warn", "error", "info", "debug"] {
        console.set(level, log_function(ctx, level)?)?;
    }
    globals.set("console", console)?;
    require::install(ctx, lua_dir.to_path_buf())
}

/// Returns a function that writes its arguments, joined by spaces, to the
/// debug log under `level`.
fn log_function<'js>(ctx: &Ctx<'js>, level: &'static str) -> rquickjs::Result<Function<'js>> {
    Function::new(ctx.clone(), move |Rest(parts): Rest<Coerced<String>>| {
        let message = parts
            .into_iter()
            .map(|part| part.0)
            .collect::<Vec<_>>()
            .join(" ");
        tracing::debug!(level, %message, "a module's script logged a message");
    })
}
