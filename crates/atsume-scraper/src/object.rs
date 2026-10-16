//! Lua objects whose fields read and write Rust state.
//!
//! Modules treat the injected objects (`MODULE`, `MANGAINFO`, `HTTP`) as plain
//! records, as in `MANGAINFO.Title = x`. An [`Object`] maps each field name to
//! a place in shared Rust state, so writes from Lua land where Rust reads them.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use mlua::{Lua, MetaMethod, UserData, UserDataMethods, Value};

use crate::sandbox::text_of;

/// Returns the place in the state a field reads and writes.
pub(crate) type Accessor<T, V> = fn(&mut T) -> &mut V;

/// How one field of an [`Object`] is read and written.
pub(crate) enum Field<T> {
    /// A string. Writing a number stores its text; anything else stores `""`.
    Text(Accessor<T, String>),
    /// An integer. Writing a float truncates it; anything but a number stores 0.
    Int(Accessor<T, i64>),
    /// A flag. Writing stores the value's Lua truthiness.
    Bool(Accessor<T, bool>),
    /// A value computed on each read and converted on each write.
    Custom {
        get: fn(&T) -> Value,
        set: fn(&Lua, &mut T, &Value),
    },
    /// A read-only value, such as a method or a nested object.
    Constant(Value),
}

/// A Lua object whose fields are backed by shared state of type `T`.
///
/// Reading an unknown field gives `nil` and writing one is ignored, as
/// upstream's objects do.
pub(crate) struct Object<T> {
    state: Rc<RefCell<T>>,
    fields: HashMap<&'static str, Field<T>>,
}

impl<T: 'static> Object<T> {
    pub(crate) fn new(state: Rc<RefCell<T>>) -> Self {
        Self {
            state,
            fields: HashMap::new(),
        }
    }

    pub(crate) fn field(mut self, name: &'static str, field: Field<T>) -> Self {
        self.fields.insert(name, field);
        self
    }

    pub(crate) fn into_value(self, lua: &Lua) -> mlua::Result<Value> {
        lua.create_userdata(self).map(Value::UserData)
    }

    fn get(&self, lua: &Lua, key: &Value) -> mlua::Result<Value> {
        let Some(field) = field_name(key).and_then(|name| self.fields.get(name.as_str())) else {
            return Ok(Value::Nil);
        };
        let mut state = self.state.borrow_mut();
        let value = match field {
            Field::Text(place) => Value::String(lua.create_string(place(&mut state).as_str())?),
            Field::Int(place) => Value::Integer(*place(&mut state)),
            Field::Bool(place) => Value::Boolean(*place(&mut state)),
            Field::Custom { get, .. } => get(&state),
            Field::Constant(value) => value.clone(),
        };
        Ok(value)
    }

    fn set(&self, lua: &Lua, key: &Value, value: &Value) {
        let Some(field) = field_name(key).and_then(|name| self.fields.get(name.as_str())) else {
            return;
        };
        let mut state = self.state.borrow_mut();
        match field {
            Field::Text(place) => *place(&mut state) = text_of(lua, value).unwrap_or_default(),
            Field::Int(place) => *place(&mut state) = to_int(lua, value),
            Field::Bool(place) => *place(&mut state) = truthy(value),
            Field::Custom { set, .. } => set(lua, &mut state, value),
            Field::Constant(_) => {}
        }
    }
}

impl<T: 'static> UserData for Object<T> {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, object, key: Value| {
            object.get(lua, &key)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, object, (key, value): (Value, Value)| {
                object.set(lua, &key, &value);
                Ok(())
            },
        );
    }
}

fn field_name(key: &Value) -> Option<String> {
    match key {
        Value::String(s) => s.to_str().ok().map(|s| s.to_owned()),
        _ => None,
    }
}

/// Reports Lua truthiness: everything but `nil` and `false` is true.
pub(crate) fn truthy(value: &Value) -> bool {
    !matches!(value, Value::Nil | Value::Boolean(false))
}

/// Reads a number, or a string holding one, truncated toward zero. Anything
/// else is 0.
pub(crate) fn to_int(lua: &Lua, value: &Value) -> i64 {
    if let Value::Integer(n) = value {
        return *n;
    }
    let Ok(Some(n)) = lua.coerce_number(value.clone()) else {
        return 0;
    };
    #[expect(
        clippy::cast_possible_truncation,
        reason = "truncating toward zero is the conversion modules expect"
    )]
    let truncated = n.trunc() as i64;
    truncated
}
