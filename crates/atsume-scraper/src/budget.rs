//! The per-call CPU budget and cancellation.
//!
//! Every call into a module runs under an instruction budget and stops once
//! the scrape is cancelled. A Lua count hook enforces both, so they also stop
//! code that never calls back into Rust, such as `while true do end`. Time
//! spent in Rust, such as waiting on HTTP, does not count.

use std::cell::Cell;
use std::rc::Rc;
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};

use mlua::{HookTriggers, Lua, VmState};

/// The default budget for one call, in Lua VM instructions: several seconds
/// of pure Lua.
pub const DEFAULT_CPU_LIMIT: u64 = 2_000_000_000;

/// How often the hook runs, in instructions.
///
/// It is prime so that a loop catching the termination with `pcall` cannot
/// keep the hook firing inside the `pcall`: the point it fires at drifts
/// through the loop until it lands where nothing catches it.
const HOOK_INTERVAL: u32 = 997;

/// Cancels a scrape from any thread.
///
/// Clones share one token. Once it is cancelled, no new call into the module
/// starts, and a running call stops within [`HOOK_INTERVAL`] instructions.
#[derive(Clone, Debug, Default)]
pub struct CancelToken(Arc<AtomicBool>);

impl CancelToken {
    /// Returns a token that has not been cancelled.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Cancels the scrape. Calling it again has no further effect.
    pub fn cancel(&self) {
        self.0.store(true, Ordering::Relaxed);
    }

    /// Reports whether [`cancel`](Self::cancel) has been called.
    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        self.0.load(Ordering::Relaxed)
    }
}

/// Why the hook ended a call.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Termination {
    CpuLimit,
    Cancelled,
}

/// The state the hook reads and the runner resets around each call.
#[derive(Debug)]
pub(crate) struct Budget {
    cancel: CancelToken,
    /// The instructions the current call may use.
    limit: Cell<u64>,
    /// The instructions the current call has used, to the nearest interval.
    used: Cell<u64>,
    /// The most any one call has used, for calibrating the limit.
    peak: Cell<u64>,
    /// Why the hook ended the current call, if it did.
    tripped: Cell<Option<Termination>>,
}

impl Budget {
    pub(crate) fn new(cancel: CancelToken, limit: u64) -> Rc<Self> {
        Rc::new(Self {
            cancel,
            limit: Cell::new(limit),
            used: Cell::new(0),
            peak: Cell::new(0),
            tripped: Cell::new(None),
        })
    }

    /// Installs the hook on `lua`.
    ///
    /// The hook is global because mlua's per-thread hook does not follow into
    /// coroutines, where a module could otherwise run unbounded. Once tripped
    /// it raises at every interval, so catching it with `pcall` does not help.
    pub(crate) fn install(self: &Rc<Self>, lua: &Lua) -> mlua::Result<()> {
        let budget = Rc::clone(self);
        let triggers = HookTriggers::new().every_nth_instruction(HOOK_INTERVAL);
        lua.set_global_hook(triggers, move |_, _| budget.tick())
    }

    fn tick(&self) -> mlua::Result<VmState> {
        self.used.set(self.used.get() + u64::from(HOOK_INTERVAL));
        if self.tripped.get().is_none() {
            self.tripped.set(self.termination());
        }
        match self.tripped.get() {
            None => Ok(VmState::Continue),
            Some(Termination::Cancelled) => Err(mlua::Error::runtime("scrape cancelled")),
            Some(Termination::CpuLimit) => Err(mlua::Error::runtime("CPU limit reached")),
        }
    }

    /// Reports why the current call must end, if it must.
    fn termination(&self) -> Option<Termination> {
        if self.cancel.is_cancelled() {
            return Some(Termination::Cancelled);
        }
        (self.used.get() > self.limit.get()).then_some(Termination::CpuLimit)
    }

    /// Resets the count for a new call.
    pub(crate) fn start(&self) {
        self.used.set(0);
        self.tripped.set(None);
    }

    /// Ends a call, recording its usage, and reports whether the hook ended it.
    pub(crate) fn finish(&self) -> Option<Termination> {
        self.peak.set(self.peak.get().max(self.used.get()));
        self.tripped.get()
    }

    pub(crate) fn cancel_token(&self) -> &CancelToken {
        &self.cancel
    }

    pub(crate) fn limit(&self) -> u64 {
        self.limit.get()
    }

    pub(crate) fn set_limit(&self, limit: u64) {
        self.limit.set(limit);
    }

    pub(crate) fn peak(&self) -> u64 {
        self.peak.get()
    }
}
