//! What an expression returns.

use crate::page::Node;

/// One value an expression returned.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Item {
    /// A node of the page.
    Node(Node),
    /// An atomic value, such as a string, number or boolean, as the text
    /// internettools gives it. A map or array reads as `""`, as upstream's
    /// JSON objects do.
    Atomic(String),
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
