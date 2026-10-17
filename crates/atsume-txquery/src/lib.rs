//! Evaluates the XPath dialect FMD2's website modules are written in.
//!
//! Upstream evaluates module expressions with internettools in its XPath 4.0
//! mode. This crate evaluates them with [xee](https://github.com/Paligo/xee),
//! an XPath 3.1 engine, over pages parsed by html5ever, and converts results
//! the way internettools does.
//!
//! ```
//! use atsume_txquery::{Document, Engine};
//!
//! let engine = Engine::new();
//! let mut page = Document::parse_html(b"<p>Status: <b>Ongoing</b></p>");
//! let status = page.evaluate(&engine, "substring-after(//p, ': ')", None).unwrap();
//! assert_eq!(page.text_of(&status), "Ongoing");
//! ```

mod document;
mod engine;
mod error;
mod html;
mod json;
mod value;

pub use document::{Document, Node};
pub use engine::Engine;
pub use error::Error;
pub use value::{Item, Sequence};
