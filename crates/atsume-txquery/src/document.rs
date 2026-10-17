//! A parsed page and the expressions run against it.

use std::collections::HashMap;

use xee_xpath::context::Variables;
use xee_xpath::{Documents, Item as XeeItem, Query};
use xot::xmlname::OwnedName;

use crate::engine::{Engine, json_variable};
use crate::error::Error;
use crate::value::{Item, Sequence};
use crate::{html, json};

/// A node of a [`Document`]. It is only meaningful with the document it came
/// from.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub struct Node(xot::Node);

/// A parsed page that expressions run against.
pub struct Document {
    documents: Documents,
    root: xot::Node,
    /// The page as received. `json(*)` reads the JSON from it, because
    /// parsing a JSON response as HTML would eat any markup inside its
    /// strings and decode its entities.
    raw: String,
    /// The tree `json()` built for each text, so a module calling it in a
    /// loop parses the text once.
    json_trees: HashMap<String, Vec<xot::Node>>,
}

impl Document {
    /// Parses an HTML page. It never fails: malformed markup is repaired as a
    /// browser would.
    #[must_use]
    pub fn parse_html(html: &[u8]) -> Self {
        let mut documents = Documents::new();
        let root = html::parse(documents.xot_mut(), html);
        register(&documents, root);
        Self {
            documents,
            root,
            raw: String::from_utf8_lossy(html).into_owned(),
            json_trees: HashMap::new(),
        }
    }

    /// Returns the document node, the context an expression starts from.
    #[must_use]
    pub fn root(&self) -> Node {
        Node(self.root)
    }

    /// Evaluates `expression`, starting from `context` or the document node.
    ///
    /// Each `json(…)` call is evaluated first: its argument's text is parsed
    /// as JSON, and the call is replaced by the resulting tree.
    ///
    /// # Errors
    ///
    /// Fails if the expression does not compile, a `json()` argument is not
    /// JSON, or the expression fails while running.
    pub fn evaluate(
        &mut self,
        engine: &Engine,
        expression: &str,
        context: Option<Node>,
    ) -> Result<Sequence, Error> {
        let calls =
            json::calls(expression).map_err(|message| compile_error(expression, message))?;
        let mut rewritten = String::with_capacity(expression.len());
        let mut trees = Vec::with_capacity(calls.len());
        let mut last = 0;
        for (index, call) in calls.iter().enumerate() {
            rewritten.push_str(&expression[last..call.span.start]);
            rewritten.push('$');
            rewritten.push_str(&json_variable(index));
            trees.push(self.json_tree(engine, &expression[call.argument.clone()], context)?);
            last = call.span.end;
        }
        rewritten.push_str(&expression[last..]);
        self.run(engine, &rewritten, context, &trees)
            .map_err(|e| e.for_expression(expression))
    }

    /// Evaluates a `json()` argument and returns the tree of the JSON its text
    /// holds.
    fn json_tree(
        &mut self,
        engine: &Engine,
        argument: &str,
        context: Option<Node>,
    ) -> Result<Vec<xot::Node>, Error> {
        let whole_page = matches!(argument.trim(), "*" | "." | "/" | "/*")
            && context.is_none_or(|node| node.0 == self.root);
        let text = if whole_page {
            self.raw.clone()
        } else {
            let value = self.evaluate(engine, argument, context)?;
            self.text_of(&value)
        };
        if let Some(tree) = self.json_trees.get(&text) {
            return Ok(tree.clone());
        }
        let values = json::parse(&text)
            .map_err(|message| evaluate_error(argument, format!("invalid JSON: {message}")))?;
        let tree = json::build(self.documents.xot_mut(), &values);
        for &document in &tree {
            register(&self.documents, document);
        }
        self.json_trees.insert(text, tree.clone());
        Ok(tree)
    }

    /// Runs a compiled expression with each `$jsonN` bound to its tree.
    fn run(
        &mut self,
        engine: &Engine,
        expression: &str,
        context: Option<Node>,
        trees: &[Vec<xot::Node>],
    ) -> Result<Sequence, Error> {
        let query = engine.compile(expression, trees.len())?;
        let mut variables = Variables::default();
        for (index, tree) in trees.iter().enumerate() {
            let name = OwnedName::name(&json_variable(index));
            variables.insert(name, tree.iter().copied().collect());
        }
        let mut builder = query.dynamic_context_builder(&self.documents);
        builder.context_node(context.map_or(self.root, |node| node.0));
        builder.variables(variables);
        let dynamic = builder.build();
        let result = query
            .execute_with_context(&mut self.documents, &dynamic)
            .map_err(|e| evaluate_error(expression, e.to_string()))?;
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

/// Registers a document node, which paths through it need to resolve the
/// root. A failure is logged and leaves the tree usable for plain steps.
fn register(documents: &Documents, root: xot::Node) {
    if let Err(error) = documents.documents().borrow_mut().add_root(None, root) {
        tracing::debug!(%error, "a parsed tree could not be registered");
    }
}

fn compile_error(expression: &str, message: String) -> Error {
    Error::Compile {
        expression: expression.to_owned(),
        message,
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
