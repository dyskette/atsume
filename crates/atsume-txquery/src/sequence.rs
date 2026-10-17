//! What an expression returns.

use crate::page::Node;

/// One value an expression returned.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Item {
    /// A node of the page.
    Node(Node),
    /// A string, number or boolean, held as the text internettools gives for
    /// it. A map or an array reads as `""`, because upstream reads a JSON
    /// object that way.
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

    /// Returns the values, consuming the sequence.
    #[must_use]
    pub fn into_items(self) -> Vec<Item> {
        self.items
    }

    /// Returns how many values there are.
    #[must_use]
    pub fn len(&self) -> usize {
        self.items.len()
    }

    /// Reports whether the sequence holds no values.
    #[must_use]
    pub fn is_empty(&self) -> bool {
        self.items.is_empty()
    }
}
