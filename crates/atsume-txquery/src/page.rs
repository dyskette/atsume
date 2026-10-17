//! A parsed page and the expressions run against it.

use std::collections::HashMap;
use std::panic::{self, AssertUnwindSafe};
use std::sync::atomic::{AtomicU64, Ordering};

use xee_xpath::context::Variables;
use xee_xpath::query::SequenceQuery;
use xee_xpath::{Documents, Item as XeeItem, Query};
use xot::xmlname::OwnedName;

use crate::dialect::json_variable;
use crate::engine::Engine;
use crate::error::Error;
use crate::value::{Item, Sequence};
use crate::{html, json};

/// A node of a [`Page`].
///
/// It remembers which page it came from. Read on another page it has no
/// text, attributes or properties, and as an expression's context it is an
/// error.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Node {
    page: u64,
    node: xot::Node,
}

/// The number the next page is given, so its nodes can be told apart.
static NEXT_PAGE_ID: AtomicU64 = AtomicU64::new(0);

/// A parsed page that expressions run against.
pub struct Page {
    id: u64,
    documents: Documents,
    root: xot::Node,
    /// The page as received, which `json(*)` falls back to when the parsed
    /// page's text is not JSON.
    raw: String,
    /// The tree `json()` built for each text, so a module calling it in a
    /// loop parses the text once.
    json_trees: HashMap<String, Vec<xot::Node>>,
}

impl Page {
    /// Parses an HTML page. It never fails: malformed markup is repaired as a
    /// browser would.
    #[must_use]
    pub fn parse_html(html: &[u8]) -> Self {
        let mut documents = Documents::new();
        let root = html::parse(documents.xot_mut(), html);
        register(&documents, root);
        Self {
            id: NEXT_PAGE_ID.fetch_add(1, Ordering::Relaxed),
            documents,
            root,
            raw: String::from_utf8_lossy(html).into_owned(),
            json_trees: HashMap::new(),
        }
    }

    /// Replaces the page with another, as upstream's `ParseHTML` does.
    ///
    /// The new page is parsed beside the old one, so nodes already read from
    /// the old page stay valid and keep reading from it.
    pub fn replace_html(&mut self, html: &[u8]) {
        self.root = html::parse(self.documents.xot_mut(), html);
        register(&self.documents, self.root);
        self.raw = String::from_utf8_lossy(html).into_owned();
    }

    /// Returns the document node, the context an expression starts from.
    #[must_use]
    pub fn root(&self) -> Node {
        self.node(self.root)
    }

    fn node(&self, node: xot::Node) -> Node {
        Node {
            page: self.id,
            node,
        }
    }

    /// Returns the tree node behind `node`, if it belongs to this page.
    fn own(&self, node: Node) -> Option<xot::Node> {
        (node.page == self.id).then_some(node.node)
    }

    /// Evaluates `expression`, starting from `context` or the document node.
    ///
    /// internettools' syntax is rewritten into standard XPath, and each
    /// `json(…)` argument is evaluated first and its text parsed as JSON; the
    /// expression then runs with the trees bound in place of the calls.
    ///
    /// # Errors
    ///
    /// Fails if the expression does not compile, a `json()` argument is not
    /// JSON, the context is a node of another page, or the expression
    /// fails while running.
    pub fn evaluate(
        &mut self,
        engine: &Engine,
        expression: &str,
        context: Option<Node>,
    ) -> Result<Sequence, Error> {
        if context.is_some_and(|node| self.own(node).is_none()) {
            let message = "the context node belongs to another page".to_owned();
            return Err(evaluate_error(expression, message));
        }
        let prepared = engine.prepare(expression)?;
        let trees = prepared
            .json_arguments
            .iter()
            .map(|argument| self.json_tree(engine, argument, context))
            .collect::<Result<Vec<_>, _>>()?;
        self.run(&prepared.query, expression, context, &trees)
    }

    /// Evaluates a `json()` argument and returns the tree of the JSON its text
    /// holds.
    ///
    /// The text is the parsed page's, as upstream reads it, so markup inside
    /// a JSON string is dropped and entities are decoded. When the argument is
    /// the whole page and that text is not JSON, the page as received is
    /// tried too: parsing it as HTML can swallow part of a JSON response, as a
    /// `<!--` inside one of its strings does.
    fn json_tree(
        &mut self,
        engine: &Engine,
        argument: &str,
        context: Option<Node>,
    ) -> Result<Vec<xot::Node>, Error> {
        let value = self.evaluate(engine, argument, context)?;
        let text = self.joined_text(&value);
        let parsed = match self.tree_of(&text) {
            Err(_) if self.is_whole_page(argument, context) => {
                let raw = std::mem::take(&mut self.raw);
                let tree = self.tree_of(&raw);
                self.raw = raw;
                tree
            }
            parsed => parsed,
        };
        parsed.map_err(|message| evaluate_error(argument, format!("invalid JSON: {message}")))
    }

    /// Reports whether a `json()` argument reads the whole page.
    fn is_whole_page(&self, argument: &str, context: Option<Node>) -> bool {
        matches!(argument.trim(), "*" | "." | "/" | "/*")
            && context.is_none_or(|node| node.node == self.root)
    }

    /// Parses `text` as JSON into a tree, reusing the tree of an earlier call
    /// with the same text.
    fn tree_of(&mut self, text: &str) -> Result<Vec<xot::Node>, String> {
        if let Some(tree) = self.json_trees.get(text) {
            return Ok(tree.clone());
        }
        let values = json::parse(text)?;
        let tree = json::build(self.documents.xot_mut(), &values);
        for &document in &tree {
            register(&self.documents, document);
        }
        self.json_trees.insert(text.to_owned(), tree.clone());
        Ok(tree)
    }

    /// Runs a prepared expression with each `$jsonN` bound to its tree.
    /// `expression` is the text the module wrote, for error messages.
    fn run(
        &mut self,
        query: &SequenceQuery,
        expression: &str,
        context: Option<Node>,
        trees: &[Vec<xot::Node>],
    ) -> Result<Sequence, Error> {
        let mut variables = Variables::default();
        for (index, tree) in trees.iter().enumerate() {
            let name = OwnedName::name(&json_variable(index));
            variables.insert(name, tree.iter().copied().collect());
        }
        let mut builder = query.dynamic_context_builder(&self.documents);
        builder.context_node(context.map_or(self.root, |node| node.node));
        builder.variables(variables);
        let dynamic = builder.build();
        // xee panics on a few arithmetic edge cases, such as round() to a huge
        // negative precision, and a module's expression must not end the scrape.
        let documents = &mut self.documents;
        let outcome = panic::catch_unwind(AssertUnwindSafe(|| {
            query.execute_with_context(documents, &dynamic)
        }));
        let Ok(result) = outcome else {
            return Err(evaluate_error(expression, "the engine panicked".to_owned()));
        };
        let result = result.map_err(|e| evaluate_error(expression, e.to_string()))?;
        let items = result.iter().map(|item| self.item(item)).collect();
        Ok(Sequence { items })
    }

    /// Converts a result. A map or array has no text in xee, and reads as
    /// `""`, as an object does upstream.
    fn item(&self, item: XeeItem) -> Item {
        match item {
            XeeItem::Node(node) => Item::Node(self.node(node)),
            other => Item::Atomic(other.string_value(self.documents.xot()).unwrap_or_default()),
        }
    }

    /// Returns an item's text: a node's full text content, or a value as it is.
    #[must_use]
    pub fn text(&self, item: &Item) -> String {
        match item {
            Item::Node(node) => self
                .own(*node)
                .map(|node| self.documents.xot().string_value(node))
                .unwrap_or_default(),
            Item::Atomic(text) => text.clone(),
        }
    }

    /// Returns a sequence's text as internettools gives it: every item's text,
    /// joined with nothing between.
    #[must_use]
    pub fn joined_text(&self, sequence: &Sequence) -> String {
        sequence.items.iter().map(|item| self.text(item)).collect()
    }

    /// Returns an element's attribute, if it has one by that name. Case is
    /// ignored, as upstream's `GetAttribute` ignores it.
    #[must_use]
    pub fn attribute(&self, node: Node, name: &str) -> Option<String> {
        let node = self.own(node)?;
        let xot = self.documents.xot();
        let attributes = xot.attributes(node);
        let found = attributes
            .iter()
            .find(|(attribute, _)| xot.local_name_str(*attribute).eq_ignore_ascii_case(name));
        found.map(|(_, value)| value.clone())
    }

    /// Returns the child element named `name`, which is how a property of a
    /// JSON object is read.
    #[must_use]
    pub fn property(&self, node: Node, name: &str) -> Option<Node> {
        let node = self.own(node)?;
        let xot = self.documents.xot();
        xot.children(node)
            .find(|&child| {
                xot.element(child)
                    .is_some_and(|element| xot.local_name_str(element.name()) == name)
            })
            .map(|child| self.node(child))
    }
}

/// Registers a document node, which paths through it need to resolve the
/// root. A failure is logged and leaves the tree usable for plain steps.
fn register(documents: &Documents, root: xot::Node) {
    if let Err(error) = documents.documents().borrow_mut().add_root(None, root) {
        tracing::debug!(%error, "a parsed tree could not be registered");
    }
}

fn evaluate_error(expression: &str, message: String) -> Error {
    Error::Evaluate {
        expression: expression.to_owned(),
        message,
    }
}

#[cfg(test)]
mod tests;
