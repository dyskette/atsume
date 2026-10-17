//! The per-call CPU budget, which also enforces cancellation.
//!
//! Every call into a module runs under an instruction budget and stops once
//! the scrape is cancelled. A Lua count hook enforces both, so they also stop
//! code that never calls back into Rust, such as `while true do end`. Time
//! spent in Rust, such as waiting on HTTP, does not count.

use std::cell::Cell;
use std::rc::Rc;

use mlua::{HookTriggers, Lua, VmState};

use crate::cancel::CancelToken;

/// The default budget for one call, in Lua VM instructions: several seconds
/// of pure Lua, so a runaway loop stops soon. A calibration test keeps it well
/// above decoding a large JSON response with upstream's pure-Lua decoder.
pub(crate) const DEFAULT_CPU_LIMIT: u64 = 1_000_000_000;

/// How often the hook runs, in instructions, until it ends a call.
const HOOK_INTERVAL: u32 = 1000;

/// Why the hook ended a call.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Termination {
    CpuLimit,
    Cancelled,
}

/// The state the hook reads and the runtime resets around each call.
#[derive(Debug)]
pub(crate) struct Budget {
    cancel: CancelToken,
    /// The instructions the current call may use.
    limit: u64,
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
            limit,
            used: Cell::new(0),
            peak: Cell::new(0),
            tripped: Cell::new(None),
        })
    }

    /// Installs the hook on `lua`.
    ///
    /// The hook is global because mlua's per-thread hook does not follow into
    /// coroutines, where a module could otherwise run unbounded.
    pub(crate) fn install(self: &Rc<Self>, lua: &Lua) -> mlua::Result<()> {
        self.hook_every(lua, HOOK_INTERVAL)
    }

    fn hook_every(self: &Rc<Self>, lua: &Lua, instructions: u32) -> mlua::Result<()> {
        let budget = Rc::clone(self);
        let triggers = HookTriggers::new().every_nth_instruction(instructions);
        lua.set_global_hook(triggers, move |lua, _| budget.tick(lua))
    }

    /// Counts an interval, and raises once the call must end.
    ///
    /// After ending a call the hook fires on every instruction of the running
    /// thread, so a module that catches the error with `pcall` is ended by the
    /// first instruction outside it.
    fn tick(self: &Rc<Self>, lua: &Lua) -> mlua::Result<VmState> {
        if self.tripped.get().is_none() {
            self.used.set(self.used.get() + u64::from(HOOK_INTERVAL));
            self.tripped.set(self.termination());
        }
        let message = match self.tripped.get() {
            None => return Ok(VmState::Continue),
            Some(Termination::Cancelled) => "scrape cancelled",
            Some(Termination::CpuLimit) => "CPU limit reached",
        };
        self.hook_every(lua, 1)?;
        Err(mlua::Error::runtime(message))
    }

    /// Reports why the current call must end, if it must.
    fn termination(&self) -> Option<Termination> {
        if self.cancel.is_cancelled() {
            return Some(Termination::Cancelled);
        }
        (self.used.get() > self.limit).then_some(Termination::CpuLimit)
    }

    /// Resets the count for a new call, restoring the normal interval if the
    /// last call was ended.
    pub(crate) fn start(self: &Rc<Self>, lua: &Lua) -> mlua::Result<()> {
        self.used.set(0);
        if self.tripped.take().is_some() {
            self.install(lua)?;
        }
        Ok(())
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
        self.limit
    }

    #[cfg(test)]
    pub(crate) fn peak(&self) -> u64 {
        self.peak.get()
    }
}
