//! `HTTP.Document`, the body of the last response.
//!
//! Upstream's is a memory stream. Modules pass it straight to `CreateTXQuery`,
//! read it with `HTTP.Document.ToString()`, and rewrite it in place, for
//! instance to descramble an image. So Lua sees an object over a buffer kept
//! in the runtime's state, which also converts to a string.

use mlua::{Lua, MetaMethod, UserData, UserDataMethods, Value};

use crate::runtime::bind::{Place, state};

/// The Lua face of a body kept in the runtime's state `S`.
pub(crate) struct DocumentHandle<S> {
    place: Place<S, Vec<u8>>,
}

impl<S> Clone for DocumentHandle<S> {
    fn clone(&self) -> Self {
        *self
    }
}

impl<S> Copy for DocumentHandle<S> {}

impl<S: 'static> DocumentHandle<S> {
    pub(crate) fn new(place: Place<S, Vec<u8>>) -> Self {
        Self { place }
    }

    /// Returns a copy of the body.
    pub(crate) fn bytes(self, lua: &Lua) -> mlua::Result<Vec<u8>> {
        let mut owner = state::<S>(lua)?;
        Ok((self.place)(&mut *owner).clone())
    }

    fn text(self, lua: &Lua) -> mlua::Result<mlua::LuaString> {
        let mut owner = state::<S>(lua)?;
        lua.create_string((self.place)(&mut *owner).as_slice())
    }

    fn size(self, lua: &Lua) -> mlua::Result<i64> {
        let mut owner = state::<S>(lua)?;
        Ok(i64::try_from((self.place)(&mut *owner).len()).unwrap_or(i64::MAX))
    }
}

impl<S: 'static> UserData for DocumentHandle<S> {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, document, key: Value| {
            let Value::String(key) = key else {
                return Ok(Value::Nil);
            };
            let document = *document;
            match key.to_str()?.as_ref() {
                "ToString" => {
                    let to_string = lua.create_function(move |lua, ()| document.text(lua))?;
                    Ok(Value::Function(to_string))
                }
                "Size" => Ok(Value::Integer(document.size(lua)?)),
                _ => Ok(Value::Nil),
            }
        });
        methods.add_meta_method(MetaMethod::ToString, |lua, document, ()| document.text(lua));
        methods.add_meta_method(MetaMethod::Len, |lua, document, ()| document.size(lua));
    }
}
