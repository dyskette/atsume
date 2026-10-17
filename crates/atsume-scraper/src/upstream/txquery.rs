//! `CreateTXQuery`, and the query objects through which modules read pages.
//!
//! A query object holds a parsed page. Its methods evaluate expressions with
//! [`atsume_txquery`] and convert the results as upstream's
//! `TXQueryEngineHTML` does. Upstream swallows every evaluation error, so an
//! expression that fails reads as nothing here too. The failure is logged at
//! debug level for whoever is fixing the module. See `docs/MODULES.md`.

use std::cell::RefCell;
use std::rc::Rc;

use atsume_txquery::{Engine, Item, Node, Page};
use mlua::{
    Function, Lua, LuaString, MetaMethod, MultiValue, ObjectLike, UserData, UserDataMethods, Value,
};

use crate::http::HttpSession;
use crate::runtime::convert::{lua_text, pascal_trim};
use crate::upstream::document::DocumentObject;

mod xpath_result;

use xpath_result::XPathResultObject;

/// What `XPathStringAll` puts between values when the module names no
/// separator.
const DEFAULT_SEPARATOR: &str = ", ";

/// A parsed page, shared by the query that parsed it and by every value read
/// from it.
type SharedPage = Rc<RefCell<Page>>;

/// Sets the global `CreateTXQuery`. Every query in the runtime shares one
/// engine, so an expression that a module runs in a loop is compiled only once.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let engine = Rc::new(Engine::new());
    let create = lua.create_function(move |lua, arguments: MultiValue| {
        create_query(lua, &engine, &arguments)
    })?;
    lua.globals().set("CreateTXQuery", create)
}

/// Builds a query over the page given, or over an empty page when there is no
/// argument. Any argument other than text or `HTTP.Document` gives `nil`, as it
/// does upstream.
fn create_query(
    lua: &Lua,
    engine: &Rc<Engine>,
    arguments: &MultiValue,
) -> mlua::Result<Option<QueryObject>> {
    let html = match arguments.front() {
        None => Vec::new(),
        Some(source) => match page_bytes(lua, source)? {
            Some(html) => html,
            None => return Ok(None),
        },
    };
    let page = Rc::new(RefCell::new(Page::parse_html(&html)));
    Ok(Some(QueryObject {
        engine: Rc::clone(engine),
        page,
    }))
}

/// Reads the page that a module passes, which may be a string, a number or
/// `HTTP.Document`.
fn page_bytes(lua: &Lua, source: &Value) -> mlua::Result<Option<Vec<u8>>> {
    match source {
        Value::String(text) => Ok(Some(text.as_bytes().to_vec())),
        Value::UserData(data) => match data.borrow::<DocumentObject<HttpSession>>() {
            Ok(document) => document.bytes(lua).map(Some),
            Err(_) => Ok(None),
        },
        other => Ok(lua_text(lua, other).map(String::into_bytes)),
    }
}

/// The object that `CreateTXQuery` returns.
#[derive(Clone)]
struct QueryObject {
    engine: Rc<Engine>,
    page: SharedPage,
}

impl UserData for QueryObject {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, query, key: Value| {
            let Value::String(key) = key else {
                return Ok(Value::Nil);
            };
            let method = query.method(lua, &key.to_str()?)?;
            Ok(method.map_or(Value::Nil, Value::Function))
        });
    }
}

impl QueryObject {
    /// Builds the method named `name`. Modules call these methods with a dot,
    /// so the first argument is a real one rather than the query.
    fn method(&self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let query = self.clone();
        let function = match name {
            "XPath" => lua.create_function(move |lua, (expression, context): (Value, Value)| {
                query.evaluate(lua, &expression, &context)
            }),
            "XPathString" => {
                lua.create_function(move |lua, (expression, context): (Value, Value)| {
                    query.evaluate(lua, &expression, &context)?.text(lua)
                })
            }
            "XPathCount" => {
                lua.create_function(move |lua, (expression, context): (Value, Value)| {
                    Ok(query.evaluate(lua, &expression, &context)?.count())
                })
            }
            "XPathStringAll" => {
                lua.create_function(move |lua, arguments: (Value, Value, Value)| {
                    query.string_all(lua, arguments)
                })
            }
            "XPathHREFAll" => lua.create_function(move |lua, arguments| {
                query.links_and_names(lua, arguments, LinkNameSource::Text)
            }),
            "XPathHREFTitleAll" => lua.create_function(move |lua, arguments| {
                query.links_and_names(lua, arguments, LinkNameSource::Title)
            }),
            "ParseHTML" => {
                lua.create_function(move |lua, source: Value| query.parse_html(lua, &source))
            }
            _ => return Ok(None),
        };
        function.map(Some)
    }

    /// Evaluates an expression against the page, or against the value that a
    /// module passes as the context.
    ///
    /// A context value stands for its first node, read on the page that the
    /// node came from. When the context holds no node, there is nothing to
    /// start from, so the result is empty, as upstream's evaluation error would
    /// make it.
    fn evaluate(
        &self,
        lua: &Lua,
        expression: &Value,
        context: &Value,
    ) -> mlua::Result<XPathResultObject> {
        let expression = lua_text(lua, expression).unwrap_or_default();
        let Some(context) = XPathResultObject::from_lua_value(context) else {
            let items = self.evaluate_or_nothing(&self.page, &expression, None)?;
            return Ok(XPathResultObject::new(Rc::clone(&self.page), items));
        };
        let Some(node) = context.first_node() else {
            return Ok(XPathResultObject::new(context.shared_page(), Vec::new()));
        };
        let items = self.evaluate_or_nothing(&context.shared_page(), &expression, Some(node))?;
        Ok(XPathResultObject::new(context.shared_page(), items))
    }

    /// Runs an expression on `page`. A failure is logged, and the expression
    /// then returns no items.
    fn evaluate_or_nothing(
        &self,
        page: &SharedPage,
        expression: &str,
        context: Option<Node>,
    ) -> mlua::Result<Vec<Item>> {
        let mut page = page
            .try_borrow_mut()
            .map_err(|_| mlua::Error::runtime("the page is already being read"))?;
        match page.evaluate(&self.engine, expression, context) {
            Ok(result) => Ok(result.into_items()),
            Err(error) => {
                tracing::debug!(%error, "an expression failed and reads as nothing");
                Ok(Vec::new())
            }
        }
    }

    /// Runs `XPathStringAll`, which upstream overloads on its second argument.
    /// That argument may be a separator, a list to fill, or, in atsume only,
    /// the context.
    ///
    /// The string forms join the trimmed values that are not empty. The list
    /// form adds every trimmed value to the list, including empty ones, and
    /// returns nothing.
    fn string_all(
        &self,
        lua: &Lua,
        (expression, second, third): (Value, Value, Value),
    ) -> mlua::Result<Option<LuaString>> {
        if XPathResultObject::from_lua_value(&second).is_some() {
            let result = self.evaluate(lua, &expression, &second)?;
            return joined(lua, &result, DEFAULT_SEPARATOR).map(Some);
        }
        if let Value::UserData(_) | Value::Table(_) = second {
            let result = self.evaluate(lua, &expression, &third)?;
            let texts = result.texts()?;
            add_all(
                &second,
                texts.iter().map(|text| pascal_trim(text).to_owned()),
            )?;
            return Ok(None);
        }
        let separator = lua_text(lua, &second).unwrap_or_else(|| DEFAULT_SEPARATOR.to_owned());
        let result = self.evaluate(lua, &expression, &third)?;
        joined(lua, &result, &separator).map(Some)
    }

    /// Runs `XPathHREFAll` or `XPathHREFTitleAll`. Each selected node's `href`
    /// goes to the first list, and its name goes to the second.
    fn links_and_names(
        &self,
        lua: &Lua,
        (expression, links, names, context): (Value, Value, Value, Value),
        source: LinkNameSource,
    ) -> mlua::Result<()> {
        let result = self.evaluate(lua, &expression, &context)?;
        let mut hrefs = Vec::new();
        let mut labels = Vec::new();
        for item in result.items() {
            hrefs.push(result.attribute_of(item, "href")?);
            labels.push(source.read(&result, item)?);
        }
        add_all(&links, hrefs)?;
        add_all(&names, labels)
    }

    /// Replaces the page, as upstream's `ParseHTML` does. An empty text leaves
    /// the page unchanged.
    fn parse_html(&self, lua: &Lua, source: &Value) -> mlua::Result<()> {
        let Some(html) = page_bytes(lua, source)?.filter(|html| !html.is_empty()) else {
            return Ok(());
        };
        let mut page = self
            .page
            .try_borrow_mut()
            .map_err(|_| mlua::Error::runtime("the page is already being read"))?;
        page.replace_html(&html);
        Ok(())
    }
}

/// Where `XPathHREFAll` and `XPathHREFTitleAll` find a link's name.
#[derive(Clone, Copy)]
enum LinkNameSource {
    /// The link's text.
    Text,
    /// The link's `title`, or its text when it has none.
    Title,
}

impl LinkNameSource {
    /// Reads the name of a selected link.
    ///
    /// Upstream gives an empty name to a link with no text, such as one that
    /// wraps only a cover image. atsume falls back to the link's `title`, which
    /// is where those sites put the name. The fallback only ever replaces an
    /// empty name.
    fn read(self, result: &XPathResultObject, item: &Item) -> mlua::Result<String> {
        let text = pascal_trim(&result.item_text(item)?).to_owned();
        let title = result.attribute_of(item, "title")?;
        let preferred = match self {
            Self::Text => text.clone(),
            Self::Title => title.clone(),
        };
        if !preferred.is_empty() {
            return Ok(preferred);
        }
        let fallback = match self {
            Self::Text => title,
            Self::Title => text,
        };
        Ok(pascal_trim(&fallback).to_owned())
    }
}

/// Joins a result's trimmed values that are not empty, as upstream's
/// `AddSeparatorString` does.
fn joined(lua: &Lua, result: &XPathResultObject, separator: &str) -> mlua::Result<LuaString> {
    let texts = result.texts()?;
    let values: Vec<&str> = texts
        .iter()
        .map(|text| pascal_trim(text))
        .filter(|text| !text.is_empty())
        .collect();
    lua.create_string(values.join(separator))
}

/// Adds each value to a list that a module passed, through the list's own
/// `Add` method, so that any of the host's lists can be filled. A missing list
/// is skipped, because modules pass only the lists they need.
fn add_all(list: &Value, values: impl IntoIterator<Item = String>) -> mlua::Result<()> {
    let add: Function = match list {
        Value::Nil => return Ok(()),
        Value::UserData(data) => data.get("Add")?,
        Value::Table(table) => table.get("Add")?,
        other => {
            let message = format!("a list was expected, got {}", other.type_name());
            return Err(mlua::Error::runtime(message));
        }
    };
    for value in values {
        add.call::<()>(value)?;
    }
    Ok(())
}

#[cfg(test)]
mod tests;
