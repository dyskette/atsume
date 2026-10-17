//! What an expression returns.

use crate::document::Node;

/// One value an expression returned.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Item {
    /// A node of the document.
    Node(Node),
    /// Any other value, as the text internettools gives it: a number, a
    /// boolean, a string. A JSON object reads as `""`, as upstream's does.
    Value(String),
}

/// The values an expression returned, in order.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Sequence {
    pub(crate) items: Vec<Item>,
}

impl Sequence {
    /// Returns the values.
    #[must_use]
    pub fn items(&self) -> &[Item] {
        &self.items
    }

    /// Returns the values, giving up the sequence.
    #[must_use]
    pub fn into_items(self) -> Vec<Item> {
        self.items
    }

    /// Returns how many values there are.
    #[must_use]
    pub fn len(&self) -> usize {
        self.items.len()
    }

    /// Reports whether there are none.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }
}
