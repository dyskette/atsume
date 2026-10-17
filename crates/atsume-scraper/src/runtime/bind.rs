//! Exposing scrape state to modules as objects.
//!
//! Each piece of state a scrape keeps, such as the selected site or the HTTP
//! session, is a plain Rust value stored in the Lua runtime. The objects
//! modules see (`MODULE`, `HTTP`) hold no state of their own: every field
//! access borrows the value from the runtime, so Rust and Lua always see the
//! same data.

use mlua::{
    AnyUserData, AppDataRefMut, Lua, MetaMethod, UserData, UserDataFields, UserDataMethods, Value,
};

use crate::runtime::convert::{text_of, to_int, truthy};

/// Returns the place in a state value that a field reads and writes.
pub(crate) type Place<S, V> = fn(&mut S) -> &mut V;

/// Borrows the runtime's state of type `S`.
///
/// A conflicting borrow, which only a binding calling back into another
/// binding could cause, is reported as a Lua error rather than a panic.
pub(crate) fn state<S: 'static>(lua: &Lua) -> mlua::Result<AppDataRefMut<'_, S>> {
    let name = std::any::type_name::<S>()
        .rsplit("::")
        .next()
        .unwrap_or_default();
    match lua.try_app_data_mut::<S>() {
        Ok(Some(state)) => Ok(state),
        Ok(None) => Err(mlua::Error::runtime(format!(
            "{name} is not available here"
        ))),
        Err(_) => Err(mlua::Error::runtime(format!("{name} is already in use"))),
    }
}

/// Adds a string field. Writing a number stores its text, and anything else
/// stores `""`.
pub(crate) fn text_field<U: UserData, S: 'static, F: UserDataFields<U>>(
    fields: &mut F,
    name: &'static str,
    place: Place<S, String>,
) {
    fields.add_field_function_get(name, move |lua, _: AnyUserData| {
        lua.create_string(place(&mut *state::<S>(lua)?).as_str())
    });
    fields.add_field_function_set(name, move |lua, _: AnyUserData, value: Value| {
        *place(&mut *state::<S>(lua)?) = text_of(lua, &value).unwrap_or_default();
        Ok(())
    });
}

/// Adds an integer field. Writing truncates a float, and anything but a
/// number stores 0.
pub(crate) fn int_field<U: UserData, S: 'static, F: UserDataFields<U>>(
    fields: &mut F,
    name: &'static str,
    place: Place<S, i64>,
) {
    fields.add_field_function_get(name, move |lua, _: AnyUserData| {
        Ok(*place(&mut *state::<S>(lua)?))
    });
    fields.add_field_function_set(name, move |lua, _: AnyUserData, value: Value| {
        *place(&mut *state::<S>(lua)?) = to_int(lua, &value);
        Ok(())
    });
}

/// Adds a flag field. Writing stores the value's Lua truthiness.
pub(crate) fn bool_field<U: UserData, S: 'static, F: UserDataFields<U>>(
    fields: &mut F,
    name: &'static str,
    place: Place<S, bool>,
) {
    fields.add_field_function_get(name, move |lua, _: AnyUserData| {
        Ok(*place(&mut *state::<S>(lua)?))
    });
    fields.add_field_function_set(name, move |lua, _: AnyUserData, value: Value| {
        *place(&mut *state::<S>(lua)?) = truthy(&value);
        Ok(())
    });
}

/// Makes unknown fields read as `nil` and ignores writes to them, as
/// upstream's objects do. mlua consults these only for keys that are not
/// declared fields or methods.
pub(crate) fn lenient<U: UserData, M: UserDataMethods<U>>(methods: &mut M) {
    methods.add_meta_function(MetaMethod::Index, |_, _: (AnyUserData, Value)| {
        Ok(Value::Nil)
    });
    methods.add_meta_function(MetaMethod::NewIndex, |_, _: (AnyUserData, Value, Value)| {
        Ok(())
    });
}
