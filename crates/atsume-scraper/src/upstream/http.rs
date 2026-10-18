//! The `HTTP` object that modules see, which works on the runtime's
//! [`HttpSession`].

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::http::{HttpSession, Method};
use crate::runtime::bind::{
    bool_field, borrow_state, ignore_unknown_fields, int_field, text_field,
};
use crate::runtime::convert::{bad_argument, lua_text, text_arg};
use crate::upstream::document::DocumentObject;
use crate::upstream::string_list::ListObject;

/// The `HTTP` global, which reads and writes the runtime's [`HttpSession`].
pub(crate) struct HttpObject;

impl UserData for HttpObject {
    /// Exposes the request settings (`MimeType`, `UserAgent`, `RetryCount`),
    /// what the last request got back (`LastURL`, `ResultCode`, `Terminated`),
    /// and the `Headers`, `Cookies` and `Document` objects.
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "MimeType", |h: &mut HttpSession| {
            &mut h.request_content_type
        });
        text_field(fields, "UserAgent", |h: &mut HttpSession| &mut h.user_agent);
        text_field(fields, "LastURL", |h: &mut HttpSession| {
            &mut h.last_request.final_url
        });
        int_field(fields, "RetryCount", |h: &mut HttpSession| {
            &mut h.max_retries
        });
        int_field(fields, "ResultCode", |h: &mut HttpSession| {
            &mut h.last_request.status
        });
        bool_field(fields, "Terminated", |h: &mut HttpSession| &mut h.cancelled);
        fields.add_field_function_get("Headers", |_, _: AnyUserData| {
            Ok(ListObject::<HttpSession>::new(|h| &mut h.headers))
        });
        fields.add_field_function_get("Cookies", |_, _: AnyUserData| {
            Ok(ListObject::<HttpSession>::new(|h| &mut h.cookies))
        });
        fields.add_field_function_get("Document", |_, _: AnyUserData| {
            Ok(DocumentObject::<HttpSession>::new(|h| &mut h.response_body))
        });
    }

    /// Adds the request functions `GET`, `HEAD`, `POST` and `XHR`, which return
    /// whether the request succeeded, and the functions that reset the session
    /// or manage its cookies. `SetProxy` is accepted and ignored.
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
            borrow_state::<HttpSession>(lua)?.reset();
            Ok(())
        });
        methods.add_function("ResetBasic", |lua, ()| {
            borrow_state::<HttpSession>(lua)?.reset();
            Ok(())
        });
        methods.add_function("ClearCookies", clear_cookies);
        methods.add_function("GetCookies", |lua, ()| {
            Ok(borrow_state::<HttpSession>(lua)?.cookies.items().join("; "))
        });
        methods.add_function("AddServerCookies", add_server_cookies);
        // Whether to use a proxy is the host's decision, so the module's
        // request is ignored.
        methods.add_function("SetProxy", |_, _: mlua::MultiValue| Ok(()));
        ignore_unknown_fields(methods);
    }
}

/// Sends a request from Lua. The URL comes first, followed by a body for
/// `POST`.
fn send(
    lua: &Lua,
    method: Method,
    url: &Value,
    body: Option<&Value>,
    prepare: fn(&mut HttpSession),
) -> mlua::Result<bool> {
    let url = lua_text(lua, url).ok_or_else(|| bad_argument(1, "string", url))?;
    let body = body
        .and_then(|body| lua_text(lua, body))
        .unwrap_or_default();
    let mut http = borrow_state::<HttpSession>(lua)?;
    prepare(&mut http);
    http.send(method, &url, &body).map_err(mlua::Error::runtime)
}

/// Sends a `POST` body as a form, unless the module chose a content type.
fn default_to_form_body(http: &mut HttpSession) {
    if http.request_content_type.is_empty() {
        "application/x-www-form-urlencoded".clone_into(&mut http.request_content_type);
    }
}

/// Marks an `XHR` call as an in-page request, which some servers answer
/// differently.
fn announce_in_page_request(http: &mut HttpSession) {
    http.headers.set_value("X-Requested-With", "XMLHttpRequest");
}

/// Implements `ClearCookies()`, which modules call through either `HTTP` or
/// `MODULE`.
pub(crate) fn clear_cookies(lua: &Lua, (): ()) -> mlua::Result<()> {
    borrow_state::<HttpSession>(lua)?.clear_cookies();
    Ok(())
}

/// Implements `AddServerCookies(url, cookie)`. Upstream takes the cookie as the
/// second argument.
pub(crate) fn add_server_cookies(lua: &Lua, (_, cookie): (Value, Value)) -> mlua::Result<()> {
    let cookie = text_arg(lua, &cookie, 2)?;
    borrow_state::<HttpSession>(lua)?.cookies.add(cookie);
    Ok(())
}
