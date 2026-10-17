//! A parsed page and the expressions run against it.

use xee_xpath::{Documents, Item as XeeItem, Query};

use crate::engine::Engine;
use crate::error::Error;
use crate::html;
use crate::value::{Item, Sequence};

/// A node of a [`Document`]. It is only meaningful with the document it came
/// from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Node(xot::Node);

/// A parsed page that expressions run against.
pub struct Document {
    documents: Documents,
    root: xot::Node,
}

impl Document {
    /// Parses an HTML page. It never fails: malformed markup is repaired as a
    /// browser would.
    #[must_use]
    pub fn parse_html(html: &[u8]) -> Self {
        let mut documents = Documents::new();
        let root = html::parse(documents.xot_mut(), html);
        if let Err(error) = documents.documents().borrow_mut().add_root(None, root) {
            tracing::debug!(%error, "the parsed page could not be registered");
        }
        Self { documents, root }
    }

    /// Returns the document node, the context an expression starts from.
    #[must_use]
    pub fn root(&self) -> Node {
        Node(self.root)
    }

    /// Evaluates `expression`, starting from `context` or the document node.
    ///
    /// # Errors
    ///
    /// Fails if the expression does not compile, or fails while running.
    pub fn evaluate(
        &mut self,
        engine: &Engine,
        expression: &str,
        context: Option<Node>,
    ) -> Result<Sequence, Error> {
        let query = engine.compile(expression)?;
        let context = context.map_or(self.root, |node| node.0);
        let result = query
            .execute(&mut self.documents, context)
            .map_err(|e| Error::Evaluate {
                expression: expression.to_owned(),
                message: e.to_string(),
            })?;
        let items = result.iter().map(|item| self.item(item)).collect();
        Ok(Sequence { items })
    }

    /// Converts a result. A map or array has no text in xee, and reads as
    /// `""`, as an object does upstream.
    fn item(&self, item: XeeItem) -> Item {
        match item {
            XeeItem::Node(node) => Item::Node(Node(node)),
            other => Item::Value(other.string_value(self.documents.xot()).unwrap_or_default()),
        }
    }

    /// Returns an item's text: a node's full text content, or a value as it is.
    #[must_use]
    pub fn text(&self, item: &Item) -> String {
        match item {
            Item::Node(node) => self.documents.xot().string_value(node.0),
            Item::Value(text) => text.clone(),
        }
    }

    /// Returns a sequence's text as internettools gives it: every item's text,
    /// joined with nothing between.
    #[must_use]
    pub fn text_of(&self, sequence: &Sequence) -> String {
        sequence.items.iter().map(|item| self.text(item)).collect()
    }

    /// Returns an element's attribute, if it has one by that name.
    #[must_use]
    pub fn attribute(&self, node: Node, name: &str) -> Option<String> {
        let xot = self.documents.xot();
        let name = xot.name(name)?;
        xot.attributes(node.0).get(name).cloned()
    }
}

#[cfg(test)]
mod tests;
