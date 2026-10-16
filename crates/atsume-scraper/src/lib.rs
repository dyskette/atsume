//! Runs the website modules from [FMD2](https://github.com/dazedcat19/FMD2).
//!
//! This crate hosts those modules in a sandboxed Lua 5.4 runtime ([`sandbox`])
//! with a CPU budget and cancellation on every call ([`budget`]).
//!
//! The modules are GPL-2.0-only, which is incompatible with atsume's
//! GPL-3.0-only, so they are cloned at run time and never built in. See
//! `docs/MODULES.md`.

#[expect(dead_code, reason = "the runner that uses the budget is ported next")]
pub mod budget;
mod error;
pub mod sandbox;

pub use budget::CancelToken;
pub use error::{Error, Result};
