//! Shows a [`StringList`] to modules. Modules index the list from 0, and they
//! read `name=value` pairs, such as HTTP headers, through
//! `list.Values['name']`.

use mlua::{Function, Lua, MetaMethod, UserData, UserDataMethods, Value};

use crate::runtime::bind::{Place, borrow_state};
use crate::runtime::convert::{int_arg, lua_int, text_arg};
use crate::string_list::{StringList, split_lines};

/// The Lua object for a list that is kept in the runtime's state `S`.
///
/// The object holds only the place where the list lives, so the list itself
/// stays a plain value that Rust code reads directly.
pub(crate) struct ListObject<S> {
    place: Place<S, StringList>,
}

impl<S> Clone for ListObject<S> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<S> Copy for ListObject<S> {}

impl<S: 'static> ListObject<S> {
    pub(crate) fn new(place: Place<S, StringList>) -> Self {
        Self { place }
    }

    fn with<R>(self, lua: &Lua, act: impl FnOnce(&mut StringList) -> R) -> mlua::Result<R> {
        Ok(act((self.place)(&mut *borrow_state::<S>(lua)?)))
    }

    fn index(self, lua: &Lua, key: &Value) -> mlua::Result<Value> {
        if let Value::Integer(_) | Value::Number(_) = key {
            let item = self.with(lua, |list| list.get(lua_int(lua, key)).to_owned())?;
            return Ok(Value::String(lua.create_string(item)?));
        }
        let Value::String(name) = key else {
            return Ok(Value::Nil);
        };
        let text = |text: String| lua.create_string(text).map(Value::String);
        match name.to_str()?.as_ref() {
            "Count" => Ok(Value::Integer(self.with(lua, |list| list.count())?)),
            "Text" => text(self.with(lua, |list| list.items().join("\r\n"))?),
            "CommaText" => text(self.with(lua, |list| list.comma_text())?),
            "Values" => Ok(Value::UserData(lua.create_userdata(ValuesObject(self))?)),
            method => Ok(self
                .method(lua, method)?
                .map_or(Value::Nil, Value::Function)),
        }
    }

    /// Builds the method named `name`. Modules call these methods with a dot,
    /// so the first argument is a real one rather than the list.
    fn method(self, lua: &Lua, name: &str) -> mlua::Result<Option<Function>> {
        let function = match name {
            "Add" => lua.create_function(move |lua, item: Value| {
                let item = text_arg(lua, &item, 1)?;
                self.with(lua, |list| list.add(item))
            }),
            "Clear" => lua.create_function(move |lua, ()| self.with(lua, StringList::clear)),
            "Reverse" => lua.create_function(move |lua, ()| self.with(lua, StringList::reverse)),
            "Delete" => lua.create_function(move |lua, index: Value| {
                let index = int_arg(lua, &index, 1)?;
                self.with(lua, |list| list.delete(index))
            }),
            "IndexOf" => lua.create_function(move |lua, item: Value| {
                let item = text_arg(lua, &item, 1)?;
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
            let item = text_arg(lua, value, 3)?;
            return self.with(lua, |list| list.put(lua_int(lua, key), item));
        }
        let Value::String(name) = key else {
            return Ok(());
        };
        match name.to_str()?.as_ref() {
            "CommaText" => {
                let text = text_arg(lua, value, 3)?;
                self.with(lua, |list| list.set_comma_text(&text))
            }
            "Text" => {
                let lines = split_lines(&text_arg(lua, value, 3)?);
                self.with(lua, |list| list.replace_all(lines))
            }
            _ => Ok(()),
        }
    }
}

impl<S: 'static> UserData for ListObject<S> {
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

/// The `list.Values` object, which reads and writes the list's `name=value`
/// pairs by name.
struct ValuesObject<S>(ListObject<S>);

impl<S: 'static> UserData for ValuesObject<S> {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, values, name: Value| {
            let name = text_arg(lua, &name, 2)?;
            let value = values.0.with(lua, |list| list.value_of(&name).to_owned())?;
            lua.create_string(value)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, values, (name, value): (Value, Value)| {
                let name = text_arg(lua, &name, 2)?;
                let value = text_arg(lua, &value, 3)?;
                values.0.with(lua, |list| list.set_value(&name, &value))
            },
        );
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::string_list::StringList;

    #[test]
    fn lua_indexes_from_zero() {
        struct Lists {
            list: StringList,
        }
        let lua = Lua::new();
        lua.set_app_data(Lists {
            list: StringList::default(),
        });
        let handle = ListObject::<Lists>::new(|lists| &mut lists.list);
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
