//! Runs the website modules from [FMD2](https://github.com/dazedcat19/FMD2).
//!
//! This crate hosts those modules in a sandboxed Lua 5.4 runtime ([`sandbox`])
//! with a CPU budget and cancellation on every call ([`budget`]). [`Host`]
//! loads a module file, and the [`Runner`] it returns runs its handlers.
//!
//! The modules are GPL-2.0-only, which is incompatible with atsume's
//! GPL-3.0-only, so they are cloned at run time and never built in. See
//! `docs/MODULES.md`.

mod bind;
pub mod budget;
mod convert;
mod document;
mod error;
mod globals;
mod host;
pub mod http;
mod libs;
pub mod module;
pub mod sandbox;
mod site;
pub mod strings;
#[cfg(test)]
mod testing;

pub use budget::{CancelToken, Cancelled};
pub use error::{Error, ErrorKind, Result};
pub use host::{AccountStatus, Host, Runner};
pub use module::{Event, Module, ModuleOption, OptionKind, OptionValue};
