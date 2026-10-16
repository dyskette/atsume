//! Pascal's `TStringList`, which modules use for links, names, page lists,
//! headers and cookies.
//!
//! Lua indexes it from 0, and reads `name=value` pairs, such as HTTP headers,
//! through `list.Values['name']`.

use mlua::{Function, Lua, MetaMethod, UserData, UserDataMethods, Value};

use crate::bind::{Place, state};
use crate::convert::{int_arg, string_arg, to_int};
use crate::module::split_lines;

/// An ordered list of strings.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub(crate) struct Strings {
    items: Vec<String>,
}

impl Strings {
    /// Returns the items.
    #[must_use]
    pub(crate) fn items(&self) -> &[String] {
        &self.items
    }

    /// Appends an item.
    pub(crate) fn add(&mut self, item: impl Into<String>) {
        self.items.push(item.into());
    }

    /// Removes every item.
    pub(crate) fn clear(&mut self) {
        self.items.clear();
    }

    /// Replaces every item.
    pub(crate) fn set(&mut self, items: Vec<String>) {
        self.items = items;
    }

    /// Returns the item at a 0-based index, or `""` when out of range.
    #[must_use]
    pub(crate) fn get(&self, index: i64) -> &str {
        usize::try_from(index)
            .ok()
            .and_then(|i| self.items.get(i))
            .map_or("", String::as_str)
    }

    /// Writes the item at a 0-based index, growing the list as needed.
    pub(crate) fn put(&mut self, index: i64, item: String) {
        let Ok(index) = usize::try_from(index) else {
            return;
        };
        if self.items.len() <= index {
            self.items.resize(index + 1, String::new());
        }
        self.items[index] = item;
    }

    /// Removes the item at a 0-based index, if there is one.
    pub(crate) fn delete(&mut self, index: i64) {
        if let Some(i) = usize::try_from(index)
            .ok()
            .filter(|&i| i < self.items.len())
        {
            self.items.remove(i);
        }
    }

    /// Returns the 0-based index of the first item equal to `item`, or -1.
    #[must_use]
    pub(crate) fn index_of(&self, item: &str) -> i64 {
        let position = self.items.iter().position(|it| it == item);
        position.map_or(-1, |i| i64::try_from(i).unwrap_or(i64::MAX))
    }

    /// Reads the value of the `name=value` pair named `name`, ignoring case.
    #[must_use]
    pub(crate) fn value(&self, name: &str) -> &str {
        self.position_of(name)
            .and_then(|i| self.items[i].split_once('='))
            .map_or("", |(_, value)| value)
    }

    /// Writes a `name=value` pair, replacing any with the same name. An empty
    /// value removes the pair.
    pub(crate) fn set_value(&mut self, name: &str, value: &str) {
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

    /// Returns the `name=value` pairs as separate names and values.
    pub(crate) fn pairs(&self) -> impl Iterator<Item = (&str, &str)> {
        self.items
            .iter()
            .filter_map(|item| item.split_once('='))
            .map(|(name, value)| (name.trim(), value))
    }

    fn position_of(&self, name: &str) -> Option<usize> {
        self.items.iter().position(|item| {
            item.split_once('=')
                .is_some_and(|(key, _)| key.trim().eq_ignore_ascii_case(name))
        })
    }

    fn count(&self) -> i64 {
        i64::try_from(self.items.len()).unwrap_or(i64::MAX)
    }

    /// Renders the list as Pascal's `CommaText`: comma-separated, with an item
    /// quoted when it holds a comma, a quote or a space.
    #[must_use]
    pub(crate) fn comma_text(&self) -> String {
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
    pub(crate) fn set_comma_text(&mut self, text: &str) {
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

/// The Lua face of a list kept in the runtime's state `S`.
///
/// It holds only where the list lives, so the list itself stays a plain value
/// that Rust code reads directly.
pub(crate) struct ListHandle<S> {
    place: Place<S, Strings>,
}

impl<S> Clone for ListHandle<S> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<S> Copy for ListHandle<S> {}

impl<S: 'static> ListHandle<S> {
    pub(crate) fn new(place: Place<S, Strings>) -> Self {
        Self { place }
    }

    fn with<R>(self, lua: &Lua, act: impl FnOnce(&mut Strings) -> R) -> mlua::Result<R> {
        Ok(act((self.place)(&mut *state::<S>(lua)?)))
    }

    fn index(self, lua: &Lua, key: &Value) -> mlua::Result<Value> {
        if let Value::Integer(_) | Value::Number(_) = key {
            let item = self.with(lua, |list| list.get(to_int(lua, key)).to_owned())?;
            return Ok(Value::String(lua.create_string(item)?));
        }
        let Value::String(name) = key else {
            return Ok(Value::Nil);
        };
        let text = |text: String| lua.create_string(text).map(Value::String);
        match name.to_str()?.as_ref() {
            "Count" => Ok(Value::Integer(self.with(lua, |list| list.count())?)),
            "Text" => text(self.with(lua, |list| list.items.join("\r\n"))?),
            "CommaText" => text(self.with(lua, |list| list.comma_text())?),
            "Values" => Ok(Value::UserData(lua.create_userdata(ValuesHandle(self))?)),
            method => Ok(self
                .method(lua, method)?
                .map_or(Value::Nil, Value::Function)),
        }
    }

    /// Builds the method named `name`. Modules call them with a dot, so the
    /// arguments start with the first real one.
    fn method(self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let function = match name {
            "Add" => lua.create_function(move |lua, item: Value| {
                let item = string_arg(lua, &item, 1)?;
                self.with(lua, |list| list.add(item))
            }),
            "Clear" => lua.create_function(move |lua, ()| self.with(lua, Strings::clear)),
            "Reverse" => {
                lua.create_function(move |lua, ()| self.with(lua, |list| list.items.reverse()))
            }
            "Delete" => lua.create_function(move |lua, index: Value| {
                let index = int_arg(lua, &index, 1)?;
                self.with(lua, |list| list.delete(index))
            }),
            "IndexOf" => lua.create_function(move |lua, item: Value| {
                let item = string_arg(lua, &item, 1)?;
                self.with(lua, |list| list.index_of(&item))
            }),
            "Get" => lua.create_function(move |lua, index: Value| {
                let index = int_arg(lua, &index, 1)?;
                let item = self.with(lua, |list| list.get(index).to_owned())?;
                lua.create_string(item)
            }),
            _ => return Ok(None),
        };
        function.map(Some)
    }

    fn new_index(self, lua: &Lua, key: &Value, value: &Value) -> mlua::Result<()> {
        if let Value::Integer(_) | Value::Number(_) = key {
            let item = string_arg(lua, value, 3)?;
            return self.with(lua, |list| list.put(to_int(lua, key), item));
        }
        let Value::String(name) = key else {
            return Ok(());
        };
        match name.to_str()?.as_ref() {
            "CommaText" => {
                let text = string_arg(lua, value, 3)?;
                self.with(lua, |list| list.set_comma_text(&text))
            }
            "Text" => {
                let lines = split_lines(&string_arg(lua, value, 3)?);
                self.with(lua, |list| list.set(lines))
            }
            _ => Ok(()),
        }
    }
}

impl<S: 'static> UserData for ListHandle<S> {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, list, key: Value| {
            list.index(lua, &key)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, list, (key, value): (Value, Value)| list.new_index(lua, &key, &value),
        );
        methods.add_meta_method(MetaMethod::Len, |lua, list, ()| {
            list.with(lua, |list| list.count())
        });
    }
}

/// `list.Values`: the list's `name=value` pairs, read and written by name.
struct ValuesHandle<S>(ListHandle<S>);

impl<S: 'static> UserData for ValuesHandle<S> {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, values, name: Value| {
            let name = string_arg(lua, &name, 2)?;
            let value = values.0.with(lua, |list| list.value(&name).to_owned())?;
            lua.create_string(value)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, values, (name, value): (Value, Value)| {
                let name = string_arg(lua, &name, 2)?;
                let value = string_arg(lua, &value, 3)?;
                values.0.with(lua, |list| list.set_value(&name, &value))
            },
        );
    }
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
        struct Lists {
            list: Strings,
        }
        let lua = Lua::new();
        lua.set_app_data(Lists {
            list: Strings::default(),
        });
        let handle = ListHandle::<Lists>::new(|lists| &mut lists.list);
        lua.globals().set("L", handle).unwrap();
        let source = "L.Add('a'); L.Add(2); L[3] = 'd'; L.Values['k'] = 'v'
            return L[0] .. L.Get(1) .. '|' .. L.Count .. '|' .. #L .. '|' .. L.IndexOf('d')
                .. '|' .. L.Values['k']";
        let got: String = lua.load(source).eval().unwrap();
        assert_eq!(got, "a2|5|5|3|v");
        let lists = lua.app_data_ref::<Lists>().unwrap();
        assert_eq!(lists.list.items(), ["a", "2", "", "d", "k=v"]);
    }
}
