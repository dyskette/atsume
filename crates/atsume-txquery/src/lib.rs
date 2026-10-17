//! Evaluates expressions written in the XPath dialect that FMD2's website
//! modules use.
//!
//! FMD2 evaluates these expressions with internettools, a Pascal library, in
//! its XPath 4.0 mode. This crate evaluates them with
//! [xee](https://github.com/Paligo/xee), an XPath 3.1 engine, on pages that
//! html5ever has parsed. It converts the results the way internettools does,
//! so modules get the values they expect.
//!
//! The crate is named after `TXQuery`, the query object that FMD2 modules
//! create with `CreateTXQuery`. Its comments use "upstream" for FMD2 together
//! with the internettools engine that FMD2 runs expressions on.
//!
//! ```
//! use atsume_txquery::{Engine, Page};
//!
//! let engine = Engine::new();
//! let mut page = Page::parse_html(b"<p>Status: <b>Ongoing</b></p>");
//! let status = page.evaluate(&engine, "substring-after(//p, ': ')", None).unwrap();
//! assert_eq!(page.sequence_text(&status), "Ongoing");
//! ```
//!
//! Evaluating an expression takes two steps. First, the [`Engine`] prepares
//! it, once per expression: `rewrite` translates internettools' syntax into
//! standard XPath and lists the arguments of any `json()` calls, and xee
//! compiles the result. Then the [`Page`] runs it. Each `json()` argument is
//! evaluated, `json` parses the argument's text into a tree, and the compiled
//! query runs over the page with those trees bound to variables.
//! `docs/MODULES.md` explains where and why the crate follows upstream.

mod css;
mod engine;
mod error;
mod html;
mod json;
mod page;
mod rewrite;
mod sequence;

pub use engine::Engine;
pub use error::Error;
pub use page::{Node, Page};
pub use sequence::{Item, Sequence};
