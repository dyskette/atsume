//! Runs the website modules from [FMD2](https://github.com/dazedcat19/FMD2).
//!
//! This crate hosts those modules in a sandboxed Lua 5.4 runtime ([`sandbox`])
//! with a CPU budget and cancellation on every call ([`budget`]). [`Host`]
//! loads a module file, and the [`Runner`] it returns runs its handlers.
//!
//! The modules are GPL-2.0-only, which is incompatible with atsume's
//! GPL-3.0-only, so they are cloned at run time and never built in. See
//! `docs/MODULES.md`.

pub mod budget;
mod error;
mod host;
mod libs;
pub mod module;
mod object;
pub mod sandbox;

pub use budget::CancelToken;
pub use error::{Error, Result};
pub use host::{Account, AccountStatus, Host, Runner};
pub use module::{Module, ModuleOption, OptionKind, OptionValue};
