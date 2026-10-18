//! The result object that `XPath` returns, which holds a sequence of nodes and
//! values.
//!
//! Upstream gives modules one kind of object for any result, whether it is a
//! list of nodes, a single node or a string. `Get(i)` reads one item as
//! another such object, and `Get()` iterates over the items in a generic
//! `for`.

use std::cell::{Cell, Ref};
use std::rc::Rc;

use atsume_txquery::{Item, Node, Page};
use mlua::{Function, Lua, LuaString, MetaMethod, UserData, UserDataMethods, Value};

use super::SharedPage;
use crate::runtime::convert::{lua_int, lua_text};

/// The items an expression returned, with the page they came from.
#[derive(Clone)]
pub(super) struct XPathResultObject {
    page: SharedPage,
    items: Rc<[Item]>,
}

impl XPathResultObject {
    pub(super) fn new(page: SharedPage, items: Vec<Item>) -> Self {
        Self {
            page,
            items: items.into(),
        }
    }

    /// Returns the result object a Lua value holds, if it holds one.
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

    /// Returns the node that the object stands for where upstream wants a
    /// single node. That is its first item, if the item is a node.
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
        Ok(self.items.iter().map(|item| page.item_text(item)).collect())
    }

    /// Returns the object's text as upstream's `ToString` gives it, with every
    /// item's text joined and nothing between them.
    pub(super) fn text(&self, lua: &Lua) -> mlua::Result<LuaString> {
        lua.create_string(self.texts()?.concat())
    }

    /// Returns one item's text.
    pub(super) fn item_text(&self, item: &Item) -> mlua::Result<String> {
        Ok(self.page()?.item_text(item))
    }

    /// Returns an item's attribute. Returns `""` when the item has no such
    /// attribute or is not an element.
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

    /// Returns the item at a 1-based position as an object of its own. A
    /// position out of range gives an empty object instead of `nil`, as it
    /// does upstream.
    fn item_at(&self, position: i64) -> Self {
        let index = usize::try_from(position - 1).ok();
        let item = index.and_then(|index| self.items.get(index));
        Self::new(self.shared_page(), item.into_iter().cloned().collect())
    }

    /// Returns the property `name` of the first item, which is how modules
    /// read JSON objects. Anything without that property gives an empty
    /// object.
    fn property(&self, name: &str) -> mlua::Result<Self> {
        let property = match self.first_node() {
            Some(node) => self.page()?.property(node, name),
            None => None,
        };
        let items = property.map(Item::Node).into_iter().collect();
        Ok(Self::new(self.shared_page(), items))
    }

    /// Builds the function that a generic `for` calls to get each item in
    /// turn.
    fn iterator(&self, lua: &Lua) -> mlua::Result<Function> {
        let result = self.clone();
        let next_position = Cell::new(1);
        lua.create_function(move |_, ()| {
            let position = next_position.get();
            if position > result.count() {
                return Ok(None);
            }
            next_position.set(position + 1);
            Ok(Some(result.item_at(position)))
        })
    }

    /// Builds the method named `name`. Modules call these methods with a dot,
    /// so the first argument is a real one rather than the result.
    fn method(&self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let result = self.clone();
        let function = match name {
            "Get" => lua.create_function(move |lua, position: Option<Value>| match position {
                Some(position) => {
                    let item = result.item_at(lua_int(lua, &position));
                    Ok(Value::UserData(lua.create_userdata(item)?))
                }
                None => result.iterator(lua).map(Value::Function),
            }),
            "GetAttribute" => lua.create_function(move |lua, attribute: Value| {
                let attribute = lua_text(lua, &attribute).unwrap_or_default();
                match result.first_node() {
                    Some(node) => result.attribute_of(&Item::Node(node), &attribute),
                    None => Ok(String::new()),
                }
            }),
            "GetProperty" => lua.create_function(move |lua, property: Value| {
                result.property(&lua_text(lua, &property).unwrap_or_default())
            }),
            "ToString" => lua.create_function(move |lua, ()| result.text(lua)),
            _ => return Ok(None),
        };
        function.map(Some)
    }
}

impl UserData for XPathResultObject {
    /// Reads `Count` as the number of items, and any other name as the method
    /// of that name, or `nil` if there is none.
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, result, key: Value| {
            let Value::String(key) = key else {
                return Ok(Value::Nil);
            };
            let key = key.to_str()?;
            if &*key == "Count" {
                return Ok(Value::Integer(result.count()));
            }
            let method = result.method(lua, &key)?;
            Ok(method.map_or(Value::Nil, Value::Function))
        });
    }
}
