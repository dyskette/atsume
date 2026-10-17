//! `CreateTXQuery`, and the query objects modules read pages through.
//!
//! A query object holds a parsed page. Its methods evaluate expressions with
//! [`atsume_txquery`] and convert the results as upstream's
//! `TXQueryEngineHTML` does. Upstream swallows every evaluation error, so an
//! expression that fails reads as nothing here too, and is logged at debug
//! level for whoever is fixing the module. See `docs/MODULES.md`.

use std::cell::RefCell;
use std::rc::Rc;

use atsume_txquery::{Document, Engine, Item, Node};
use mlua::{
    Function, Lua, LuaString, MetaMethod, MultiValue, ObjectLike, UserData, UserDataMethods, Value,
};

use crate::convert::{pascal_trim, text_of};
use crate::document::DocumentHandle;
use crate::http::HttpSession;

mod value;

use value::ValueObject;

/// What `XPathStringAll` joins values with when the module names nothing.
const DEFAULT_SEPARATOR: &str = ", ";

/// A parsed page, shared by the query that parsed it and every value read
/// from it.
type SharedDocument = Rc<RefCell<Document>>;

/// Sets the global `CreateTXQuery`. Every query of the runtime shares one
/// engine, so an expression a module runs in a loop compiles once.
pub(crate) fn register(lua: &Lua) -> mlua::Result<()> {
    let engine = Rc::new(Engine::new());
    let create = lua.create_function(move |lua, arguments: MultiValue| {
        create_query(lua, &engine, &arguments)
    })?;
    lua.globals().set("CreateTXQuery", create)
}

/// Builds a query over the page given, or over an empty page when there is
/// no argument. Anything but text or `HTTP.Document` gives `nil`, as
/// upstream's does.
fn create_query(
    lua: &Lua,
    engine: &Rc<Engine>,
    arguments: &MultiValue,
) -> mlua::Result<Option<QueryObject>> {
    let html = match arguments.front() {
        None => Vec::new(),
        Some(source) => match page_text(lua, source)? {
            Some(html) => html,
            None => return Ok(None),
        },
    };
    let document = Rc::new(RefCell::new(Document::parse_html(&html)));
    Ok(Some(QueryObject {
        engine: Rc::clone(engine),
        document,
    }))
}

/// Reads the page a module passes: a string, a number, or `HTTP.Document`.
fn page_text(lua: &Lua, source: &Value) -> mlua::Result<Option<Vec<u8>>> {
    match source {
        Value::String(text) => Ok(Some(text.as_bytes().to_vec())),
        Value::UserData(data) => match data.borrow::<DocumentHandle<HttpSession>>() {
            Ok(document) => document.bytes(lua).map(Some),
            Err(_) => Ok(None),
        },
        other => Ok(text_of(lua, other).map(String::into_bytes)),
    }
}

/// The object `CreateTXQuery` returns.
#[derive(Clone)]
struct QueryObject {
    engine: Rc<Engine>,
    document: SharedDocument,
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
    /// Builds the method named `name`. Modules call them with a dot, so the
    /// arguments start with the first real one.
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
                query.links_and_names(lua, arguments, Names::Text)
            }),
            "XPathHREFTitleAll" => lua.create_function(move |lua, arguments| {
                query.links_and_names(lua, arguments, Names::Title)
            }),
            "ParseHTML" => {
                lua.create_function(move |lua, source: Value| query.parse_html(lua, &source))
            }
            _ => return Ok(None),
        };
        function.map(Some)
    }

    /// Evaluates an expression against the page, or against the value a
    /// module passes as the context.
    ///
    /// A context value stands for its first node, read on the page it came
    /// from. With no node in it there is nothing to start from, and the
    /// result is empty, as upstream's evaluation error would make it.
    fn evaluate(
        &self,
        lua: &Lua,
        expression: &Value,
        context: &Value,
    ) -> mlua::Result<ValueObject> {
        let expression = text_of(lua, expression).unwrap_or_default();
        let Some(context) = ValueObject::from_lua_value(context) else {
            let items = self.run(&self.document, &expression, None)?;
            return Ok(ValueObject::new(Rc::clone(&self.document), items));
        };
        let Some(node) = context.first_node() else {
            return Ok(ValueObject::new(context.document(), Vec::new()));
        };
        let items = self.run(&context.document(), &expression, Some(node))?;
        Ok(ValueObject::new(context.document(), items))
    }

    /// Runs an expression on `document`, logging a failure and returning no
    /// items for it.
    fn run(
        &self,
        document: &SharedDocument,
        expression: &str,
        context: Option<Node>,
    ) -> mlua::Result<Vec<Item>> {
        let mut page = document
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

    /// Runs `XPathStringAll`, which upstream overloads on its second
    /// argument: a separator, a list to fill, or, in atsume, the context.
    ///
    /// The string forms join the trimmed values that are not empty. The list
    /// form adds every trimmed value, empty ones too, and returns nothing.
    fn string_all(
        &self,
        lua: &Lua,
        (expression, second, third): (Value, Value, Value),
    ) -> mlua::Result<Option<LuaString>> {
        if ValueObject::from_lua_value(&second).is_some() {
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
        let separator = text_of(lua, &second).unwrap_or_else(|| DEFAULT_SEPARATOR.to_owned());
        let result = self.evaluate(lua, &expression, &third)?;
        joined(lua, &result, &separator).map(Some)
    }

    /// Runs `XPathHREFAll` or `XPathHREFTitleAll`: each selected node's
    /// `href` goes to the first list, and its name to the second.
    fn links_and_names(
        &self,
        lua: &Lua,
        (expression, links, names, context): (Value, Value, Value, Value),
        source: Names,
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

    /// Replaces the page, unless the text given is empty, as upstream's
    /// `ParseHTML` does.
    fn parse_html(&self, lua: &Lua, source: &Value) -> mlua::Result<()> {
        let Some(html) = page_text(lua, source)?.filter(|html| !html.is_empty()) else {
            return Ok(());
        };
        let mut page = self
            .document
            .try_borrow_mut()
            .map_err(|_| mlua::Error::runtime("the page is already being read"))?;
        page.replace_html(&html);
        Ok(())
    }
}

/// Where `XPathHREFAll` and `XPathHREFTitleAll` read a link's name.
#[derive(Clone, Copy)]
enum Names {
    /// The link's text.
    Text,
    /// The link's `title`, or its text when it has none.
    Title,
}

impl Names {
    /// Reads the name of a selected link.
    ///
    /// Upstream gives a link with no text, such as one wrapping only a cover
    /// image, an empty name. atsume falls back to its `title`, which is where
    /// those sites put the name; this only ever replaces nothing.
    fn read(self, result: &ValueObject, item: &Item) -> mlua::Result<String> {
        let text = pascal_trim(&result.text_of(item)?).to_owned();
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
fn joined(lua: &Lua, result: &ValueObject, separator: &str) -> mlua::Result<LuaString> {
    let texts = result.texts()?;
    let values: Vec<&str> = texts
        .iter()
        .map(|text| pascal_trim(text))
        .filter(|text| !text.is_empty())
        .collect();
    lua.create_string(values.join(separator))
}

/// Adds each value to a list a module passed, through the list's own `Add`,
/// so any of the host's lists can be filled. A missing list is skipped,
/// because modules pass only the lists they need.
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
