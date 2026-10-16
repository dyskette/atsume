//! Pascal's `TStringList`, which modules use for links, names, page lists,
//! headers and cookies.
//!
//! Lua indexes it from 0, and reads `name=value` pairs, such as HTTP headers,
//! through `list.Values['name']`.

use std::cell::RefCell;
use std::rc::Rc;

use mlua::{Function, Lua, MetaMethod, UserData, UserDataMethods, Value};

use crate::module::split_lines;
use crate::object::to_int;
use crate::sandbox::text_of;

/// An ordered list of strings.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct Strings {
    items: Vec<String>,
}

/// A list shared between Rust and the Lua objects that expose it.
pub(crate) type StringList = Rc<RefCell<Strings>>;

impl Strings {
    /// Returns the items.
    #[must_use]
    pub fn items(&self) -> &[String] {
        &self.items
    }

    /// Appends an item.
    pub fn add(&mut self, item: impl Into<String>) {
        self.items.push(item.into());
    }

    /// Removes every item.
    pub fn clear(&mut self) {
        self.items.clear();
    }

    /// Replaces every item.
    pub fn set(&mut self, items: Vec<String>) {
        self.items = items;
    }

    /// Returns the item at a 0-based index, or `""` when out of range.
    #[must_use]
    pub fn get(&self, index: i64) -> &str {
        usize::try_from(index)
            .ok()
            .and_then(|i| self.items.get(i))
            .map_or("", String::as_str)
    }

    /// Writes the item at a 0-based index, growing the list as needed.
    pub fn put(&mut self, index: i64, item: String) {
        let Ok(index) = usize::try_from(index) else {
            return;
        };
        if self.items.len() <= index {
            self.items.resize(index + 1, String::new());
        }
        self.items[index] = item;
    }

    /// Reads the value of the `name=value` pair named `name`, ignoring case.
    #[must_use]
    pub fn value(&self, name: &str) -> &str {
        self.position_of(name)
            .and_then(|i| self.items[i].split_once('='))
            .map_or("", |(_, value)| value)
    }

    /// Writes a `name=value` pair, replacing any with the same name. An empty
    /// value removes the pair.
    pub fn set_value(&mut self, name: &str, value: &str) {
        let position = self.position_of(name);
        match (position, value.is_empty()) {
            (Some(i), true) => {
                self.items.remove(i);
            }
            (Some(i), false) => self.items[i] = format!("{name}={value}"),
            (None, false) => self.items.push(format!("{name}={value}")),
            (None, true) => {}
        }
    }

    fn position_of(&self, name: &str) -> Option<usize> {
        self.items.iter().position(|item| {
            item.split_once('=')
                .is_some_and(|(key, _)| key.trim().eq_ignore_ascii_case(name))
        })
    }

    /// Renders the list as Pascal's `CommaText`: comma-separated, with an item
    /// quoted when it holds a comma, a quote or a space.
    #[must_use]
    pub fn comma_text(&self) -> String {
        let quote = |item: &String| {
            if item.contains([',', '"', ' ']) {
                format!("\"{}\"", item.replace('"', "\"\""))
            } else {
                item.clone()
            }
        };
        self.items.iter().map(quote).collect::<Vec<_>>().join(",")
    }

    /// Replaces the items by parsing a `CommaText` value.
    pub fn set_comma_text(&mut self, text: &str) {
        self.items.clear();
        let mut current = String::new();
        let mut quoted = false;
        let mut chars = text.chars().peekable();
        while let Some(c) = chars.next() {
            match c {
                '"' if quoted && chars.peek() == Some(&'"') => {
                    current.push('"');
                    chars.next();
                }
                '"' => quoted = !quoted,
                ',' if !quoted => self.items.push(std::mem::take(&mut current)),
                _ => current.push(c),
            }
        }
        if !current.is_empty() || !self.items.is_empty() {
            self.items.push(current);
        }
    }
}

/// The Lua face of a [`StringList`].
pub(crate) struct StringsHandle(pub(crate) StringList);

impl UserData for StringsHandle {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, list, key: Value| {
            list.index(lua, &key)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, list, (key, value): (Value, Value)| list.new_index(lua, &key, &value),
        );
        methods.add_meta_method(MetaMethod::Len, |_, list, ()| {
            Ok(list.0.borrow().items.len())
        });
    }
}

impl StringsHandle {
    fn index(&self, lua: &Lua, key: &Value) -> mlua::Result<Value> {
        if let Value::Integer(_) | Value::Number(_) = key {
            let list = self.0.borrow();
            return Ok(Value::String(
                lua.create_string(list.get(to_int(lua, key)))?,
            ));
        }
        let Value::String(name) = key else {
            return Ok(Value::Nil);
        };
        let list = self.0.borrow();
        let value = match name.to_str()?.as_ref() {
            "Count" => Value::Integer(i64::try_from(list.items.len()).unwrap_or(i64::MAX)),
            "Text" => Value::String(lua.create_string(list.items.join("\r\n"))?),
            "CommaText" => Value::String(lua.create_string(list.comma_text())?),
            "Values" => Value::UserData(lua.create_userdata(ValuesHandle(Rc::clone(&self.0)))?),
            method => self
                .method(lua, method)?
                .map_or(Value::Nil, Value::Function),
        };
        Ok(value)
    }

    /// Builds the method named `name`. Modules call them with a dot, so the
    /// arguments start with the first real one.
    fn method(&self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let list = Rc::clone(&self.0);
        let function = match name {
            "Add" => lua.create_function(move |lua, item: Value| {
                list.borrow_mut().add(string_arg(lua, &item, 1)?);
                Ok(())
            }),
            "Clear" => lua.create_function(move |_, ()| {
                list.borrow_mut().clear();
                Ok(())
            }),
            "Reverse" => lua.create_function(move |_, ()| {
                list.borrow_mut().items.reverse();
                Ok(())
            }),
            "Delete" => lua.create_function(move |lua, index: Value| {
                let index = int_arg(lua, &index, 1)?;
                let mut list = list.borrow_mut();
                if let Some(i) = usize::try_from(index)
                    .ok()
                    .filter(|&i| i < list.items.len())
                {
                    list.items.remove(i);
                }
                Ok(())
            }),
            "IndexOf" => lua.create_function(move |lua, item: Value| {
                let item = string_arg(lua, &item, 1)?;
                let position = list.borrow().items.iter().position(|it| *it == item);
                Ok(position.map_or(-1, |i| i64::try_from(i).unwrap_or(i64::MAX)))
            }),
            "Get" => lua.create_function(move |lua, index: Value| {
                let index = int_arg(lua, &index, 1)?;
                lua.create_string(list.borrow().get(index))
            }),
            _ => return Ok(None),
        };
        function.map(Some)
    }

    fn new_index(&self, lua: &Lua, key: &Value, value: &Value) -> mlua::Result<()> {
        if let Value::Integer(_) | Value::Number(_) = key {
            let item = string_arg(lua, value, 3)?;
            self.0.borrow_mut().put(to_int(lua, key), item);
            return Ok(());
        }
        let Value::String(name) = key else {
            return Ok(());
        };
        match name.to_str()?.as_ref() {
            "CommaText" => self
                .0
                .borrow_mut()
                .set_comma_text(&string_arg(lua, value, 3)?),
            "Text" => self
                .0
                .borrow_mut()
                .set(split_lines(&string_arg(lua, value, 3)?)),
            _ => {}
        }
        Ok(())
    }
}

/// `list.Values`: the list's `name=value` pairs, read and written by name.
struct ValuesHandle(StringList);

impl UserData for ValuesHandle {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, values, name: Value| {
            let name = string_arg(lua, &name, 2)?;
            lua.create_string(values.0.borrow().value(&name))
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, values, (name, value): (Value, Value)| {
                let name = string_arg(lua, &name, 2)?;
                let value = string_arg(lua, &value, 3)?;
                values.0.borrow_mut().set_value(&name, &value);
                Ok(())
            },
        );
    }
}

/// Reads argument `position` as a string, accepting a number as Lua does.
pub(crate) fn string_arg(lua: &Lua, value: &Value, position: usize) -> mlua::Result<String> {
    text_of(lua, value).ok_or_else(|| bad_argument(position, "string", value))
}

/// Reads argument `position` as an integer, accepting a float or a numeric
/// string and truncating it.
fn int_arg(lua: &Lua, value: &Value, position: usize) -> mlua::Result<i64> {
    match lua.coerce_number(value.clone()) {
        Ok(Some(_)) => Ok(to_int(lua, value)),
        _ => Err(bad_argument(position, "number", value)),
    }
}

pub(crate) fn bad_argument(position: usize, expected: &str, got: &Value) -> mlua::Error {
    mlua::Error::runtime(format!(
        "bad argument #{position} ({expected} expected, got {})",
        got.type_name()
    ))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn comma_text_round_trips_quoted_items() {
        let mut list = Strings::default();
        list.set_comma_text(r#"a.jpg,b.jpg,"c,with,commas.jpg""#);
        assert_eq!(list.items(), ["a.jpg", "b.jpg", "c,with,commas.jpg"]);
        assert_eq!(list.comma_text(), r#"a.jpg,b.jpg,"c,with,commas.jpg""#);
    }

    #[test]
    fn values_are_name_value_pairs() {
        let mut list = Strings::default();
        list.set_value("Referer", "https://a.example");
        list.set_value("referer", "https://b.example");
        assert_eq!(list.items(), ["referer=https://b.example"]);
        assert_eq!(list.value("REFERER"), "https://b.example");
        list.set_value("Referer", "");
        assert_eq!(list.items(), [] as [String; 0]);
    }

    #[test]
    fn lua_indexes_from_zero() {
        let lua = Lua::new();
        let list = StringList::default();
        lua.globals()
            .set("L", StringsHandle(Rc::clone(&list)))
            .unwrap();
        let source = "L.Add('a'); L.Add(2); L[3] = 'd'; L.Values['k'] = 'v'
            return L[0] .. L.Get(1) .. '|' .. L.Count .. '|' .. #L .. '|' .. L.IndexOf('d')
                .. '|' .. L.Values['k']";
        let got: String = lua.load(source).eval().unwrap();
        assert_eq!(got, "a2|5|5|3|v");
        assert_eq!(list.borrow().items(), ["a", "2", "", "d", "k=v"]);
    }
}
