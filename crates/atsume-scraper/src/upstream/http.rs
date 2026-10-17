//! The `HTTP` object modules see, over the runtime's [`HttpSession`].

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::http::{HttpSession, Method};
use crate::runtime::bind::{bool_field, int_field, lenient, state, text_field};
use crate::runtime::convert::{bad_argument, string_arg, text_of};
use crate::upstream::document::DocumentHandle;
use crate::upstream::string_list::ListHandle;

/// `HTTP`. It holds nothing itself; every access reaches the session.
pub(crate) struct HttpObject;

impl UserData for HttpObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "MimeType", |h: &mut HttpSession| &mut h.mime_type);
        text_field(fields, "UserAgent", |h: &mut HttpSession| &mut h.user_agent);
        text_field(fields, "LastURL", |h: &mut HttpSession| &mut h.last.url);
        int_field(fields, "RetryCount", |h: &mut HttpSession| {
            &mut h.retry_count
        });
        int_field(fields, "ResultCode", |h: &mut HttpSession| {
            &mut h.last.status
        });
        bool_field(fields, "Terminated", |h: &mut HttpSession| {
            &mut h.terminated
        });
        fields.add_field_function_get("Headers", |_, _: AnyUserData| {
            Ok(ListHandle::<HttpSession>::new(|h| &mut h.headers))
        });
        fields.add_field_function_get("Cookies", |_, _: AnyUserData| {
            Ok(ListHandle::<HttpSession>::new(|h| &mut h.cookies))
        });
        fields.add_field_function_get("Document", |_, _: AnyUserData| {
            Ok(DocumentHandle::<HttpSession>::new(|h| &mut h.document))
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_function("GET", |lua, (url, _): (Value, Value)| {
            send(lua, Method::Get, &url, None, |_| {})
        });
        methods.add_function("HEAD", |lua, (url, _): (Value, Value)| {
            send(lua, Method::Head, &url, None, |_| {})
        });
        methods.add_function("POST", |lua, (url, body): (Value, Value)| {
            send(lua, Method::Post, &url, Some(&body), default_to_form_body)
        });
        methods.add_function("XHR", |lua, (url, _): (Value, Value)| {
            send(lua, Method::Get, &url, None, announce_in_page_request)
        });
        methods.add_function("Reset", |lua, ()| {
            state::<HttpSession>(lua)?.reset();
            Ok(())
        });
        methods.add_function("ResetBasic", |lua, ()| {
            state::<HttpSession>(lua)?.reset();
            Ok(())
        });
        methods.add_function("ClearCookies", clear_cookies);
        methods.add_function("GetCookies", |lua, ()| {
            Ok(state::<HttpSession>(lua)?.cookies.items().join("; "))
        });
        methods.add_function("AddServerCookies", add_server_cookies);
        // Proxying is the host's policy, not the module's.
        methods.add_function("SetProxy", |_, _: mlua::MultiValue| Ok(()));
        lenient(methods);
    }
}

/// Sends a request from Lua: the URL first, then a body for `POST`.
fn send(
    lua: &Lua,
    method: Method,
    url: &Value,
    body: Option<&Value>,
    prepare: fn(&mut HttpSession),
) -> mlua::Result<bool> {
    let url = text_of(lua, url).ok_or_else(|| bad_argument(1, "string", url))?;
    let body = body.and_then(|body| text_of(lua, body)).unwrap_or_default();
    let mut http = state::<HttpSession>(lua)?;
    prepare(&mut http);
    http.send(method, &url, &body).map_err(mlua::Error::runtime)
}

/// Sends a `POST` body as a form unless the module chose a type.
fn default_to_form_body(http: &mut HttpSession) {
    if http.mime_type.is_empty() {
        "application/x-www-form-urlencoded".clone_into(&mut http.mime_type);
    }
}

/// Marks an `XHR` as an in-page request, which some servers answer
/// differently.
fn announce_in_page_request(http: &mut HttpSession) {
    http.headers.set_value("X-Requested-With", "XMLHttpRequest");
}

/// `ClearCookies()`, which modules call through `HTTP` or `MODULE`.
pub(crate) fn clear_cookies(lua: &Lua, (): ()) -> mlua::Result<()> {
    state::<HttpSession>(lua)?.clear_cookies();
    Ok(())
}

/// `AddServerCookies(url, cookie)`. Upstream takes the cookie second.
pub(crate) fn add_server_cookies(lua: &Lua, (_, cookie): (Value, Value)) -> mlua::Result<()> {
    let cookie = string_arg(lua, &cookie, 2)?;
    state::<HttpSession>(lua)?.cookies.add(cookie);
    Ok(())
}
