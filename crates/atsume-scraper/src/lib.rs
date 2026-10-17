//! Runs the website modules from [FMD2](https://github.com/dazedcat19/FMD2).
//!
//! A [`Catalog`] lists the websites that the modules in a checkout declare.
//! Each [`Site`] opens a [`Session`] for one scrape. The session runs the
//! module in a sandboxed Lua 5.4 runtime, and every call into the module has a
//! CPU budget and can be cancelled.
//!
//! ```no_run
//! use atsume_scraper::{CancelToken, Catalog, SessionConfig};
//!
//! # fn main() -> atsume_scraper::Result<()> {
//! let catalog = Catalog::load("/data/modules/lua")?;
//! let site = catalog.site("MangaDex", None).expect("declared by the checkout");
//! let mut session = site.open(&SessionConfig::default(), CancelToken::new())?;
//! println!("{} has {} directory pages", site.declaration().name, session.directory_page_count(0)?);
//! # Ok(())
//! # }
//! ```
//!
//! The modules are GPL-2.0-only, which is incompatible with atsume's
//! GPL-3.0-only license. They are therefore cloned when atsume runs and never
//! built into it. See `docs/MODULES.md`.

mod cancel;
mod catalog;
mod error;
pub mod http;
mod links;
mod model;
mod runtime;
mod session;
mod string_list;
#[cfg(test)]
mod testing;
mod upstream;

pub use cancel::{CancelToken, Cancelled};
pub use catalog::{Catalog, LoadFailure, Site};
pub use error::{Error, ErrorKind, Result};
pub use model::{
    AccountStatus, Chapter, ChapterPage, DirectoryEntry, DirectoryPage, Event, OptionKind,
    OptionValue, SeriesInfo, SeriesStatus, SiteDeclaration, SiteOption,
};
pub use session::{Session, SessionConfig};
