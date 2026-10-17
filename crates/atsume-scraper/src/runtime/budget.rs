//! The CPU budget for each call into a module, which also enforces
//! cancellation.
//!
//! Every call into a module runs under an instruction budget, and it stops
//! once the scrape is cancelled. A Lua count hook enforces both, so they also
//! stop code that never calls back into Rust, such as `while true do end`.
//! Time spent in Rust, such as waiting on HTTP, does not count against the
//! budget.

use std::cell::Cell;
use std::rc::Rc;

use mlua::{HookTriggers, Lua, VmState};

use crate::cancel::CancelToken;

/// The default budget for one call, in Lua VM instructions. It allows several
/// seconds of pure Lua, so a runaway loop stops soon. A calibration test keeps
/// it well above what upstream's pure-Lua decoder needs for a large JSON
/// response.
pub(crate) const DEFAULT_CPU_LIMIT: u64 = 1_000_000_000;

/// How many instructions run between two runs of the hook, until the hook ends
/// a call.
const HOOK_INTERVAL: u32 = 1000;

/// Why the hook ended a call.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub(crate) enum Termination {
    CpuLimit,
    Cancelled,
}

/// The state that the hook reads and that the runtime resets around each call.
#[derive(Debug)]
pub(crate) struct Budget {
    cancel: CancelToken,
    /// How many instructions the current call may use.
    limit: u64,
    /// How many instructions the current call has used, rounded to the hook's
    /// interval.
    used: Cell<u64>,
    /// The most instructions that any one call has used. Tests use it to
    /// calibrate the limit.
    peak: Cell<u64>,
    /// Why the hook ended the current call, if it did.
    ended_by: Cell<Option<Termination>>,
}

impl Budget {
    pub(crate) fn new(cancel: CancelToken, limit: u64) -> Rc<Self> {
        Rc::new(Self {
            cancel,
            limit,
            used: Cell::new(0),
            peak: Cell::new(0),
            ended_by: Cell::new(None),
        })
    }

    /// Installs the hook on `lua`.
    ///
    /// The hook is global, because mlua's per-thread hook does not follow into
    /// coroutines, where a module could otherwise run without limit.
    pub(crate) fn install(self: &Rc<Self>, lua: &Lua) -> mlua::Result<()> {
        self.hook_every(lua, HOOK_INTERVAL)
    }

    fn hook_every(self: &Rc<Self>, lua: &Lua, instructions: u32) -> mlua::Result<()> {
        let budget = Rc::clone(self);
        let triggers = HookTriggers::new().every_nth_instruction(instructions);
        lua.set_global_hook(triggers, move |lua, _| budget.tick(lua))
    }

    /// Counts one interval, and raises an error once the call must end.
    ///
    /// After ending a call, the hook fires on every instruction of the running
    /// thread. A module that catches the error with `pcall` is therefore ended
    /// by its first instruction outside the `pcall`.
    fn tick(self: &Rc<Self>, lua: &Lua) -> mlua::Result<VmState> {
        if self.ended_by.get().is_none() {
            self.used.set(self.used.get() + u64::from(HOOK_INTERVAL));
            self.ended_by.set(self.termination());
        }
        let message = match self.ended_by.get() {
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

    /// Resets the count for a new call. If the hook ended the last call, this
    /// also restores the normal interval.
    pub(crate) fn start(self: &Rc<Self>, lua: &Lua) -> mlua::Result<()> {
        self.used.set(0);
        if self.ended_by.take().is_some() {
            self.install(lua)?;
        }
        Ok(())
    }

    /// Finishes a call, records how much it used, and reports whether the hook
    /// ended it.
    pub(crate) fn finish(&self) -> Option<Termination> {
        self.peak.set(self.peak.get().max(self.used.get()));
        self.ended_by.get()
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
