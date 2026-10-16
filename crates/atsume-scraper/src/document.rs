//! `HTTP.Document`, the body of the last response.
//!
//! Upstream's is a memory stream. Modules pass it straight to `CreateTXQuery`,
//! read it with `HTTP.Document.ToString()`, and rewrite it in place, for
//! instance to descramble an image. So it is a shared buffer that Lua sees as
//! an object which also converts to a string.

use std::cell::RefCell;
use std::rc::Rc;

use mlua::{MetaMethod, UserData, UserDataMethods, Value};

/// A response body shared between Rust and Lua.
pub(crate) type Document = Rc<RefCell<Vec<u8>>>;

/// The Lua face of a [`Document`].
pub(crate) struct DocumentHandle(pub(crate) Document);

impl UserData for DocumentHandle {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, document, key: Value| {
            let Value::String(key) = key else {
                return Ok(Value::Nil);
            };
            let value = match key.to_str()?.as_ref() {
                "ToString" => {
                    let body = Rc::clone(&document.0);
                    let to_string =
                        lua.create_function(move |lua, ()| lua.create_string(&*body.borrow()))?;
                    Value::Function(to_string)
                }
                "Size" => Value::Integer(size(&document.0)),
                _ => Value::Nil,
            };
            Ok(value)
        });
        methods.add_meta_method(MetaMethod::ToString, |lua, document, ()| {
            lua.create_string(&*document.0.borrow())
        });
        methods.add_meta_method(MetaMethod::Len, |_, document, ()| Ok(size(&document.0)));
    }
}

fn size(document: &Document) -> i64 {
    i64::try_from(document.borrow().len()).unwrap_or(i64::MAX)
}
