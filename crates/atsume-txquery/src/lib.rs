//! Evaluates the XPath dialect FMD2's website modules are written in.
//!
//! Upstream evaluates module expressions with internettools in its XPath 4.0
//! mode. This crate evaluates them with [xee](https://github.com/Paligo/xee),
//! an XPath 3.1 engine, over pages parsed by html5ever, and converts results
//! the way internettools does.
//!
//! ```
//! use atsume_txquery::{Engine, Page};
//!
//! let engine = Engine::new();
//! let mut page = Page::parse_html(b"<p>Status: <b>Ongoing</b></p>");
//! let status = page.evaluate(&engine, "substring-after(//p, ': ')", None).unwrap();
//! assert_eq!(page.joined_text(&status), "Ongoing");
//! ```
//!
//! An expression goes through two halves. The [`Engine`] prepares it once:
//! `dialect` rewrites internettools' syntax into standard XPath and lists the
//! `json()` arguments, and xee compiles the result. The [`Page`] runs it:
//! each `json()` argument is evaluated and its text read by `json` into a
//! tree, then the compiled query runs over the page with those trees bound.
//! `docs/MODULES.md` explains the choices upstream forced.

mod css;
mod dialect;
mod engine;
mod error;
mod html;
mod json;
mod page;
mod value;

pub use engine::Engine;
pub use error::Error;
pub use page::{Node, Page};
pub use value::{Item, Sequence};
