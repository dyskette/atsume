//! Parsing a page into the tree expressions run against.
//!
//! html5ever parses the page as a browser would, repairing whatever the site
//! got wrong, and this builder writes each node straight into the arena xee
//! queries. Upstream's own parser repairs pages the same way for what modules
//! rely on, such as the `tbody` a table always gets.

use std::borrow::Cow;
use std::cell::RefCell;
use std::collections::HashMap;

use html5ever::tendril::{StrTendril, TendrilSink};
use html5ever::tree_builder::{ElemName, ElementFlags, NodeOrText, QuirksMode, TreeSink};
use html5ever::{Attribute, LocalName, Namespace, QualName, local_name, ns};
use xot::{Node, Xot};

/// Parses `html` into `xot` and returns the document node.
///
/// Names are lowercase and in no namespace, so `//div` matches as modules
/// expect, and whitespace is kept. Comments and processing instructions are
/// left out, as upstream reads them. Invalid UTF-8 is replaced.
pub(crate) fn parse(xot: &mut Xot, html: &[u8]) -> Node {
    let document = xot.new_document();
    let sink = Sink {
        xot: RefCell::new(xot),
        document,
        names: RefCell::default(),
        templates: RefCell::default(),
    };
    html5ever::parse_document(sink, html5ever::ParseOpts::default())
        .from_utf8()
        .one(html)
}

/// Builds the tree as html5ever parses.
///
/// html5ever calls back through shared references, so the arena is behind a
/// `RefCell`.
struct Sink<'x> {
    xot: RefCell<&'x mut Xot>,
    document: Node,
    /// The full name of each element, which html5ever asks back for.
    names: RefCell<HashMap<Node, QualName>>,
    /// The detached node holding each `template` element's contents.
    templates: RefCell<HashMap<Node, Node>>,
}

/// An element's name as html5ever asks for it.
#[derive(Debug)]
struct OwnedName(QualName);

impl ElemName for OwnedName {
    fn ns(&self) -> &Namespace {
        &self.0.ns
    }

    fn local_name(&self) -> &LocalName {
        &self.0.local
    }
}

impl Sink<'_> {
    /// Adds a node, skipping the kinds upstream leaves out of its tree.
    fn attach(&self, place: impl FnOnce(&mut Xot, Node) -> Result<(), xot::Error>, child: Node) {
        let mut xot = self.xot.borrow_mut();
        if xot.is_comment(child) {
            return;
        }
        tolerate(place(&mut xot, child));
    }

    fn new_text(&self, text: &str) -> Node {
        self.xot.borrow_mut().new_text(text)
    }
}

impl TreeSink for Sink<'_> {
    type Handle = Node;
    type Output = Node;
    type ElemName<'a>
        = OwnedName
    where
        Self: 'a;

    fn finish(self) -> Node {
        self.document
    }

    /// Pages from the web are rarely valid; html5ever repairs them, and the
    /// repairs are expected rather than worth reporting.
    fn parse_error(&self, _: Cow<'static, str>) {}

    fn get_document(&self) -> Node {
        self.document
    }

    fn elem_name<'a>(&'a self, target: &'a Node) -> OwnedName {
        let names = self.names.borrow();
        let name = names.get(target).cloned();
        OwnedName(name.unwrap_or_else(|| QualName::new(None, ns!(), local_name!(""))))
    }

    fn create_element(&self, name: QualName, attrs: Vec<Attribute>, flags: ElementFlags) -> Node {
        let mut xot = self.xot.borrow_mut();
        let element_name = xot.add_name(&name.local);
        let element = xot.new_element(element_name);
        for attr in attrs {
            let attr_name = xot.add_name(&attr.name.local);
            xot.attributes_mut(element)
                .insert(attr_name, attr.value.to_string());
        }
        if flags.template {
            let contents_name = xot.add_name("template");
            let contents = xot.new_element(contents_name);
            self.templates.borrow_mut().insert(element, contents);
        }
        self.names.borrow_mut().insert(element, name);
        element
    }

    fn create_comment(&self, text: StrTendril) -> Node {
        self.xot.borrow_mut().new_comment(&text)
    }

    /// Upstream leaves processing instructions out, so this returns a
    /// placeholder that is never attached.
    fn create_pi(&self, _: StrTendril, _: StrTendril) -> Node {
        self.xot.borrow_mut().new_comment("")
    }

    fn append(&self, parent: &Node, child: NodeOrText<Node>) {
        let child = match child {
            NodeOrText::AppendNode(node) => node,
            NodeOrText::AppendText(text) => self.new_text(&text),
        };
        self.attach(|xot, child| xot.append(*parent, child), child);
    }

    fn append_based_on_parent_node(
        &self,
        element: &Node,
        prev_element: &Node,
        child: NodeOrText<Node>,
    ) {
        let attached = self.xot.borrow().parent(*element).is_some();
        if attached {
            self.append_before_sibling(element, child);
        } else {
            self.append(prev_element, child);
        }
    }

    /// The tree has no doctype node, as XPath has none.
    fn append_doctype_to_document(&self, _: StrTendril, _: StrTendril, _: StrTendril) {}

    fn get_template_contents(&self, target: &Node) -> Node {
        self.templates
            .borrow()
            .get(target)
            .copied()
            .unwrap_or(*target)
    }

    fn same_node(&self, x: &Node, y: &Node) -> bool {
        x == y
    }

    fn set_quirks_mode(&self, _: QuirksMode) {}

    fn append_before_sibling(&self, sibling: &Node, new_node: NodeOrText<Node>) {
        let child = match new_node {
            NodeOrText::AppendNode(node) => node,
            NodeOrText::AppendText(text) => self.new_text(&text),
        };
        self.attach(|xot, child| xot.insert_before(*sibling, child), child);
    }

    fn add_attrs_if_missing(&self, target: &Node, attrs: Vec<Attribute>) {
        let mut xot = self.xot.borrow_mut();
        for attr in attrs {
            let attr_name = xot.add_name(&attr.name.local);
            let mut attributes = xot.attributes_mut(*target);
            if attributes.get(attr_name).is_none() {
                attributes.insert(attr_name, attr.value.to_string());
            }
        }
    }

    fn remove_from_parent(&self, target: &Node) {
        tolerate(self.xot.borrow_mut().detach(*target));
    }

    fn reparent_children(&self, node: &Node, new_parent: &Node) {
        let mut xot = self.xot.borrow_mut();
        let children: Vec<Node> = xot.children(*node).collect();
        for child in children {
            tolerate(
                xot.detach(child)
                    .and_then(|()| xot.append(*new_parent, child)),
            );
        }
    }
}

/// Logs a repair the arena refused. html5ever's repairs are valid trees, so
/// this is not expected; a refusal leaves that node out rather than failing
/// the page.
fn tolerate(result: Result<(), xot::Error>) {
    if let Err(error) = result {
        tracing::debug!(%error, "a node was left out of the parsed page");
    }
}
