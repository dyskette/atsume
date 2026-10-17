//! The value object `XPath` returns: a sequence of nodes and values.
//!
//! Upstream gives modules one object for any result, whether a list of nodes,
//! one node or a string. `Get(i)` reads one item as another such object, and
//! `Get()` iterates them in a generic `for`.

use std::cell::{Cell, Ref};
use std::rc::Rc;

use atsume_txquery::{Item, Node, Page};
use mlua::{Function, Lua, LuaString, MetaMethod, UserData, UserDataMethods, Value};

use super::SharedPage;
use crate::runtime::convert::{text_of, to_int};

/// The items an expression returned, with the page they came from.
#[derive(Clone)]
pub(super) struct ValueObject {
    page: SharedPage,
    items: Rc<[Item]>,
}

impl ValueObject {
    pub(super) fn new(page: SharedPage, items: Vec<Item>) -> Self {
        Self {
            page,
            items: items.into(),
        }
    }

    /// Returns the value object a Lua value holds, if it holds one.
    pub(super) fn from_lua_value(value: &Value) -> Option<Self> {
        let Value::UserData(data) = value else {
            return None;
        };
        data.borrow::<Self>().ok().map(|object| object.clone())
    }

    pub(super) fn shared_page(&self) -> SharedPage {
        Rc::clone(&self.page)
    }

    pub(super) fn items(&self) -> &[Item] {
        &self.items
    }

    /// Returns the node the object stands for where upstream wants one: its
    /// first item, if that is a node.
    pub(super) fn first_node(&self) -> Option<Node> {
        match self.items.first() {
            Some(Item::Node(node)) => Some(*node),
            _ => None,
        }
    }

    pub(super) fn count(&self) -> i64 {
        i64::try_from(self.items.len()).unwrap_or(i64::MAX)
    }

    /// Returns each item's text.
    pub(super) fn texts(&self) -> mlua::Result<Vec<String>> {
        let page = self.page()?;
        Ok(self.items.iter().map(|item| page.text(item)).collect())
    }

    /// Returns the object's text as upstream's `ToString` gives it: every
    /// item's text, joined with nothing between.
    pub(super) fn text(&self, lua: &Lua) -> mlua::Result<LuaString> {
        lua.create_string(self.texts()?.concat())
    }

    /// Returns one item's text.
    pub(super) fn item_text(&self, item: &Item) -> mlua::Result<String> {
        Ok(self.page()?.text(item))
    }

    /// Returns an item's attribute, or `""` when it has none or is not an
    /// element.
    pub(super) fn attribute_of(&self, item: &Item, name: &str) -> mlua::Result<String> {
        let Item::Node(node) = item else {
            return Ok(String::new());
        };
        Ok(self.page()?.attribute(*node, name).unwrap_or_default())
    }

    fn page(&self) -> mlua::Result<Ref<'_, Page>> {
        self.page
            .try_borrow()
            .map_err(|_| mlua::Error::runtime("the page is already being read"))
    }

    /// Returns the item at a 1-based position as an object of its own. Out
    /// of range it is an empty object, not `nil`, as upstream's is.
    fn item_at(&self, position: i64) -> Self {
        let index = usize::try_from(position - 1).ok();
        let item = index.and_then(|index| self.items.get(index));
        Self::new(self.shared_page(), item.into_iter().cloned().collect())
    }

    /// Returns the property `name` of the first item, which modules read on
    /// JSON objects. Anything without that property gives an empty object.
    fn property(&self, name: &str) -> mlua::Result<Self> {
        let property = match self.first_node() {
            Some(node) => self.page()?.property(node, name),
            None => None,
        };
        let items = property.map(Item::Node).into_iter().collect();
        Ok(Self::new(self.shared_page(), items))
    }

    /// Builds the function a generic `for` calls for each item in turn.
    fn iterator(&self, lua: &Lua) -> mlua::Result<Function> {
        let value = self.clone();
        let next_position = Cell::new(1);
        lua.create_function(move |_, ()| {
            let position = next_position.get();
            if position > value.count() {
                return Ok(None);
            }
            next_position.set(position + 1);
            Ok(Some(value.item_at(position)))
        })
    }

    /// Builds the method named `name`. Modules call them with a dot, so the
    /// arguments start with the first real one.
    fn method(&self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let value = self.clone();
        let function = match name {
            "Get" => lua.create_function(move |lua, position: Option<Value>| match position {
                Some(position) => {
                    let item = value.item_at(to_int(lua, &position));
                    Ok(Value::UserData(lua.create_userdata(item)?))
                }
                None => value.iterator(lua).map(Value::Function),
            }),
            "GetAttribute" => lua.create_function(move |lua, attribute: Value| {
                let attribute = text_of(lua, &attribute).unwrap_or_default();
                match value.first_node() {
                    Some(node) => value.attribute_of(&Item::Node(node), &attribute),
                    None => Ok(String::new()),
                }
            }),
            "GetProperty" => lua.create_function(move |lua, property: Value| {
                value.property(&text_of(lua, &property).unwrap_or_default())
            }),
            "ToString" => lua.create_function(move |lua, ()| value.text(lua)),
            _ => return Ok(None),
        };
        function.map(Some)
    }
}

impl UserData for ValueObject {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, value, key: Value| {
            let Value::String(key) = key else {
                return Ok(Value::Nil);
            };
            let key = key.to_str()?;
            if &*key == "Count" {
                return Ok(Value::Integer(value.count()));
            }
            let method = value.method(lua, &key)?;
            Ok(method.map_or(Value::Nil, Value::Function))
        });
    }
}
