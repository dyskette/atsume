//! The globals FMD2 gives modules, each in a file named after it.
//!
//! An object here keeps no state of its own. What it reads and writes is a
//! plain Rust value kept in the runtime, so the host reads a handler's results
//! straight back; see [`crate::runtime::bind`].

pub(crate) mod declaration;
pub(crate) mod directory;
pub(crate) mod document;
pub(crate) mod globals;
pub(crate) mod http;
pub(crate) mod libs;
pub(crate) mod mangainfo;
pub(crate) mod module;
pub(crate) mod string_list;
pub(crate) mod task;
pub(crate) mod txquery;
