//! Runs the website modules from [FMD2](https://github.com/dazedcat19/FMD2).
//!
//! A [`Catalog`] lists the websites the modules in a checkout declare. Each
//! [`Site`] opens a [`Session`] for one scrape, which runs the module in a
//! sandboxed Lua 5.4 runtime with a CPU budget and cancellation on every call.
//!
//! ```no_run
//! use atsume_scraper::{CancelToken, Catalog, SessionConfig};
//!
//! # fn main() -> atsume_scraper::Result<()> {
//! let catalog = Catalog::load("/data/modules/lua")?;
//! let site = catalog.site("MangaDex", None).expect("declared by the checkout");
//! let session = site.open(&SessionConfig::default(), CancelToken::new())?;
//! println!("{} has {} directory pages", site.module().name, session.directory_pages()?);
//! # Ok(())
//! # }
//! ```
//!
//! The modules are GPL-2.0-only, which is incompatible with atsume's
//! GPL-3.0-only, so they are cloned at run time and never built in. See
//! `docs/MODULES.md`.

mod cancel;
mod catalog;
mod error;
pub mod http;
mod links;
mod model;
mod runtime;
mod session;
#[cfg(test)]
mod testing;
mod upstream;

pub use cancel::{CancelToken, Cancelled};
pub use catalog::{Catalog, LoadFailure, Site};
pub use error::{Error, ErrorKind, Result};
pub use model::{
    AccountStatus, Chapter, DirectoryEntry, DirectoryPage, Event, Module, ModuleOption, OptionKind,
    OptionValue, Page, SeriesInfo, SeriesStatus,
};
pub use session::{Session, SessionConfig};
