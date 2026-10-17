//! Parses a page into the tree that expressions run against.
//!
//! html5ever parses the page the way a browser does and repairs whatever the
//! site got wrong. The builder here writes each node straight into the arena
//! that xee queries. Upstream's own parser repairs pages the same way wherever
//! modules depend on it, for example by giving every table a `tbody`.

use std::borrow::Cow;
use std::cell::RefCell;
use std::collections::HashMap;

use html5ever::tendril::{StrTendril, TendrilSink};
use html5ever::tree_builder::{ElemName, ElementFlags, NodeOrText, QuirksMode, TreeSink};
use html5ever::{Attribute, LocalName, Namespace, QualName, local_name, ns};
use xot::Xot;

/// Parses `html` into `xot` and returns the document node.
///
/// Element and attribute names are lowercase and in no namespace, so `//div`
/// matches as modules expect, and whitespace is kept. Comments and processing
/// instructions are left out, as upstream leaves them out. Invalid UTF-8 is
/// replaced with U+FFFD.
pub(crate) fn parse(xot: &mut Xot, html: &[u8]) -> xot::Node {
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

/// Builds the tree while html5ever parses.
///
/// html5ever calls back through shared references, so the arena sits behind a
/// `RefCell`.
struct Sink<'x> {
    xot: RefCell<&'x mut Xot>,
    document: xot::Node,
    /// The full name of each element. html5ever asks for it again later.
    names: RefCell<HashMap<xot::Node, QualName>>,
    /// For each `template` element, the detached node that holds its contents.
    templates: RefCell<HashMap<xot::Node, xot::Node>>,
}

/// An element's name, in the form that html5ever asks for.
#[derive(Debug)]
struct ElementName(QualName);

impl ElemName for ElementName {
    fn ns(&self) -> &Namespace {
        &self.0.ns
    }

    fn local_name(&self) -> &LocalName {
        &self.0.local
    }
}

impl Sink<'_> {
    /// Adds a node to the tree, unless it is a kind that upstream leaves out.
    fn attach(
        &self,
        place: impl FnOnce(&mut Xot, xot::Node) -> Result<(), xot::Error>,
        child: xot::Node,
    ) {
        let mut xot = self.xot.borrow_mut();
        if xot.is_comment(child) {
            return;
        }
        log_if_refused(place(&mut xot, child));
    }

    fn new_text(&self, text: &str) -> xot::Node {
        self.xot.borrow_mut().new_text(text)
    }
}

impl TreeSink for Sink<'_> {
    type Handle = xot::Node;
    type Output = xot::Node;
    type ElemName<'a>
        = ElementName
    where
        Self: 'a;

    fn finish(self) -> xot::Node {
        self.document
    }

    /// Pages from the web are rarely valid. html5ever repairs them, and the
    /// repairs are expected, so they are not reported.
    fn parse_error(&self, _: Cow<'static, str>) {}

    fn get_document(&self) -> xot::Node {
        self.document
    }

    fn elem_name<'a>(&'a self, target: &'a xot::Node) -> ElementName {
        let names = self.names.borrow();
        let name = names.get(target).cloned();
        ElementName(name.unwrap_or_else(|| QualName::new(None, ns!(), local_name!(""))))
    }

    fn create_element(
        &self,
        name: QualName,
        attrs: Vec<Attribute>,
        flags: ElementFlags,
    ) -> xot::Node {
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

    fn create_comment(&self, text: StrTendril) -> xot::Node {
        self.xot.borrow_mut().new_comment(&text)
    }

    /// Upstream leaves processing instructions out, so this returns a
    /// placeholder that is never attached.
    fn create_pi(&self, _: StrTendril, _: StrTendril) -> xot::Node {
        self.xot.borrow_mut().new_comment("")
    }

    fn append(&self, parent: &xot::Node, child: NodeOrText<xot::Node>) {
        let child = match child {
            NodeOrText::AppendNode(node) => node,
            NodeOrText::AppendText(text) => self.new_text(&text),
        };
        self.attach(|xot, child| xot.append(*parent, child), child);
    }

    fn append_based_on_parent_node(
        &self,
        element: &xot::Node,
        prev_element: &xot::Node,
        child: NodeOrText<xot::Node>,
    ) {
        let attached = self.xot.borrow().parent(*element).is_some();
        if attached {
            self.append_before_sibling(element, child);
        } else {
            self.append(prev_element, child);
        }
    }

    /// The tree has no doctype node, because XPath has none.
    fn append_doctype_to_document(&self, _: StrTendril, _: StrTendril, _: StrTendril) {}

    fn get_template_contents(&self, target: &xot::Node) -> xot::Node {
        self.templates
            .borrow()
            .get(target)
            .copied()
            .unwrap_or(*target)
    }

    fn same_node(&self, x: &xot::Node, y: &xot::Node) -> bool {
        x == y
    }

    fn set_quirks_mode(&self, _: QuirksMode) {}

    fn append_before_sibling(&self, sibling: &xot::Node, new_node: NodeOrText<xot::Node>) {
        let child = match new_node {
            NodeOrText::AppendNode(node) => node,
            NodeOrText::AppendText(text) => self.new_text(&text),
        };
        self.attach(|xot, child| xot.insert_before(*sibling, child), child);
    }

    fn add_attrs_if_missing(&self, target: &xot::Node, attrs: Vec<Attribute>) {
        let mut xot = self.xot.borrow_mut();
        for attr in attrs {
            let attr_name = xot.add_name(&attr.name.local);
            let mut attributes = xot.attributes_mut(*target);
            if attributes.get(attr_name).is_none() {
                attributes.insert(attr_name, attr.value.to_string());
            }
        }
    }

    fn remove_from_parent(&self, target: &xot::Node) {
        log_if_refused(self.xot.borrow_mut().detach(*target));
    }

    fn reparent_children(&self, node: &xot::Node, new_parent: &xot::Node) {
        let mut xot = self.xot.borrow_mut();
        let children: Vec<xot::Node> = xot.children(*node).collect();
        for child in children {
            log_if_refused(
                xot.detach(child)
                    .and_then(|()| xot.append(*new_parent, child)),
            );
        }
    }
}

/// Logs a repair that the arena refused. html5ever's repairs always produce
/// valid trees, so a refusal is not expected. If one happens, that node is
/// left out instead of failing the whole page.
fn log_if_refused(result: Result<(), xot::Error>) {
    if let Err(error) = result {
        tracing::debug!(%error, "a node was left out of the parsed page");
    }
}
