//! A parsed page and the expressions that run against it.

use std::collections::HashMap;
use std::panic::{self, AssertUnwindSafe};
use std::sync::atomic::{AtomicU64, Ordering};

use xee_xpath::context::Variables;
use xee_xpath::query::SequenceQuery;
use xee_xpath::{Documents, Item as XeeItem, Query};
use xot::xmlname::OwnedName;

use crate::engine::Engine;
use crate::error::Error;
use crate::rewrite::json_variable_name;
use crate::sequence::{Item, Sequence};
use crate::{html, json};

/// A node of a [`Page`].
///
/// A node remembers which page it came from. On any other page it has no
/// text, attributes or properties, and using it as an expression's context is
/// an error.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Node {
    page_id: u64,
    xot_node: xot::Node,
}

/// The number that the next parsed page receives, so that nodes from different
/// pages can be told apart.
static NEXT_PAGE_ID: AtomicU64 = AtomicU64::new(0);

/// A parsed page that expressions run against.
pub struct Page {
    id: u64,
    documents: Documents,
    xot_root: xot::Node,
    /// The page exactly as it was received. `json(*)` falls back to it when
    /// the parsed page's text is not JSON.
    as_received: String,
    /// The tree that `json()` built for each text it was given. A module that
    /// calls `json()` in a loop therefore parses each text only once.
    json_trees: HashMap<String, Vec<xot::Node>>,
}

impl Page {
    /// Parses an HTML page. Parsing never fails, because malformed markup is
    /// repaired the way a browser repairs it.
    #[must_use]
    pub fn parse_html(html: &[u8]) -> Self {
        let mut documents = Documents::new();
        let xot_root = html::parse(documents.xot_mut(), html);
        register_document_root(&documents, xot_root);
        Self {
            id: NEXT_PAGE_ID.fetch_add(1, Ordering::Relaxed),
            documents,
            xot_root,
            as_received: String::from_utf8_lossy(html).into_owned(),
            json_trees: HashMap::new(),
        }
    }

    /// Replaces the page with a new one, as FMD2's `ParseHTML` does.
    ///
    /// The new page is parsed alongside the old one. Nodes that were already
    /// read from the old page therefore stay valid and keep reading from it.
    pub fn replace_html(&mut self, html: &[u8]) {
        self.xot_root = html::parse(self.documents.xot_mut(), html);
        register_document_root(&self.documents, self.xot_root);
        self.as_received = String::from_utf8_lossy(html).into_owned();
    }

    /// Returns the document node, which is where an expression starts when it
    /// is given no context.
    #[must_use]
    pub fn root(&self) -> Node {
        self.wrap(self.xot_root)
    }

    /// Wraps a tree node of this page in a [`Node`] that remembers the page.
    fn wrap(&self, xot_node: xot::Node) -> Node {
        Node {
            page_id: self.id,
            xot_node,
        }
    }

    /// Returns the tree node behind `node`, if it belongs to this page.
    fn xot_node_of(&self, node: Node) -> Option<xot::Node> {
        (node.page_id == self.id).then_some(node.xot_node)
    }

    /// Evaluates `expression`, starting from `context`, or from the document
    /// node when `context` is `None`.
    ///
    /// The expression's internettools syntax is first rewritten into standard
    /// XPath. Each `json(…)` argument is then evaluated and its text parsed as
    /// JSON. Finally the expression runs, with the resulting trees standing in
    /// for the `json()` calls.
    ///
    /// # Errors
    ///
    /// Fails if the expression does not compile, if a `json()` argument's text
    /// is not JSON, if the context is a node of another page, or if the
    /// expression fails while running.
    pub fn evaluate(
        &mut self,
        engine: &Engine,
        expression: &str,
        context: Option<Node>,
    ) -> Result<Sequence, Error> {
        if context.is_some_and(|node| self.xot_node_of(node).is_none()) {
            let message = "the context node belongs to another page".to_owned();
            return Err(evaluate_error(expression, message));
        }
        let prepared = engine.prepare(expression)?;
        let trees = prepared
            .json_arguments
            .iter()
            .map(|argument| self.evaluate_json_argument(engine, argument, context))
            .collect::<Result<Vec<_>, _>>()?;
        self.execute(&prepared.query, expression, context, &trees)
    }

    /// Evaluates a `json()` argument and parses the text it yields as JSON.
    ///
    /// The text comes from the parsed page, as it does upstream, so markup
    /// inside a JSON string is dropped and entities are decoded. When the
    /// argument selects the whole page and that text is not JSON, the page as
    /// received is tried as well. HTML parsing can swallow part of a JSON
    /// response, for example when one of its strings contains `<!--`.
    fn evaluate_json_argument(
        &mut self,
        engine: &Engine,
        argument: &str,
        context: Option<Node>,
    ) -> Result<Vec<xot::Node>, Error> {
        let value = self.evaluate(engine, argument, context)?;
        let text = self.sequence_text(&value);
        let parsed = match self.parse_json_tree(&text) {
            Err(_) if self.is_whole_page(argument, context) => {
                let as_received = std::mem::take(&mut self.as_received);
                let tree = self.parse_json_tree(&as_received);
                self.as_received = as_received;
                tree
            }
            parsed => parsed,
        };
        parsed.map_err(|message| evaluate_error(argument, format!("invalid JSON: {message}")))
    }

    /// Reports whether a `json()` argument selects the whole page.
    fn is_whole_page(&self, argument: &str, context: Option<Node>) -> bool {
        matches!(argument.trim(), "*" | "." | "/" | "/*")
            && context.is_none_or(|node| node.xot_node == self.xot_root)
    }

    /// Parses `text` as JSON into a tree. A later call with the same text
    /// reuses that tree.
    fn parse_json_tree(&mut self, text: &str) -> Result<Vec<xot::Node>, String> {
        if let Some(tree) = self.json_trees.get(text) {
            return Ok(tree.clone());
        }
        let values = json::parse(text)?;
        let tree = json::build_trees(self.documents.xot_mut(), &values);
        for &document in &tree {
            register_document_root(&self.documents, document);
        }
        self.json_trees.insert(text.to_owned(), tree.clone());
        Ok(tree)
    }

    /// Executes a compiled query with each `$jsonN` variable bound to its tree.
    /// `expression` is the text that the module wrote, and it appears only in
    /// error messages.
    fn execute(
        &mut self,
        query: &SequenceQuery,
        expression: &str,
        context: Option<Node>,
        trees: &[Vec<xot::Node>],
    ) -> Result<Sequence, Error> {
        let mut variables = Variables::default();
        for (index, tree) in trees.iter().enumerate() {
            let name = OwnedName::name(&json_variable_name(index));
            variables.insert(name, tree.iter().copied().collect());
        }
        let mut builder = query.dynamic_context_builder(&self.documents);
        builder.context_node(context.map_or(self.xot_root, |node| node.xot_node));
        builder.variables(variables);
        let dynamic_context = builder.build();
        // xee panics on a few arithmetic edge cases, such as `round()` with a
        // huge negative precision. A module's expression must never end the
        // scrape, so the panic is caught and reported as an error.
        let documents = &mut self.documents;
        let outcome_or_panic = panic::catch_unwind(AssertUnwindSafe(|| {
            query.execute_with_context(documents, &dynamic_context)
        }));
        let Ok(executed) = outcome_or_panic else {
            return Err(evaluate_error(expression, "the engine panicked".to_owned()));
        };
        let xee_sequence = executed.map_err(|e| evaluate_error(expression, e.to_string()))?;
        let items = xee_sequence
            .iter()
            .map(|item| self.convert_item(item))
            .collect();
        Ok(Sequence { items })
    }

    /// Converts an xee item into an [`Item`]. A map or an array has no text in
    /// xee, so it reads as `""`, which is how upstream reads a JSON object.
    fn convert_item(&self, item: XeeItem) -> Item {
        match item {
            XeeItem::Node(xot_node) => Item::Node(self.wrap(xot_node)),
            other => Item::Atomic(other.string_value(self.documents.xot()).unwrap_or_default()),
        }
    }

    /// Returns an item's text. For a node, that is its full text content; for
    /// an atomic value, it is the value itself.
    #[must_use]
    pub fn item_text(&self, item: &Item) -> String {
        match item {
            Item::Node(node) => self
                .xot_node_of(*node)
                .map(|xot_node| self.documents.xot().string_value(xot_node))
                .unwrap_or_default(),
            Item::Atomic(text) => text.clone(),
        }
    }

    /// Returns a sequence's text the way internettools gives it, with every
    /// item's text joined and nothing between them.
    #[must_use]
    pub fn sequence_text(&self, sequence: &Sequence) -> String {
        sequence
            .items
            .iter()
            .map(|item| self.item_text(item))
            .collect()
    }

    /// Returns the value of the element's attribute named `name`, if it has
    /// one. The name is compared ignoring case, as FMD2's `GetAttribute`
    /// compares it.
    #[must_use]
    pub fn attribute(&self, node: Node, name: &str) -> Option<String> {
        let xot_node = self.xot_node_of(node)?;
        let xot = self.documents.xot();
        let attributes = xot.attributes(xot_node);
        let found = attributes
            .iter()
            .find(|(attribute, _)| xot.local_name_str(*attribute).eq_ignore_ascii_case(name));
        found.map(|(_, value)| value.clone())
    }

    /// Returns the child element named `name`. This is how a property of a JSON
    /// object is read, because `json` builds each property as a child element.
    #[must_use]
    pub fn property(&self, node: Node, name: &str) -> Option<Node> {
        let xot_node = self.xot_node_of(node)?;
        let xot = self.documents.xot();
        xot.children(xot_node)
            .find(|&child| {
                xot.element(child)
                    .is_some_and(|element| xot.local_name_str(element.name()) == name)
            })
            .map(|child| self.wrap(child))
    }
}

/// Registers a document node with xee, which needs it to resolve paths that
/// start at the root. A failure is only logged, because the tree still works
/// for paths that do not start at the root.
fn register_document_root(documents: &Documents, root: xot::Node) {
    if let Err(error) = documents.documents().borrow_mut().add_root(None, root) {
        tracing::debug!(%error, "a parsed tree could not be registered");
    }
}

fn evaluate_error(expression: &str, message: String) -> Error {
    Error::EvaluateFailed {
        expression: expression.to_owned(),
        message,
    }
}

#[cfg(test)]
mod tests;
