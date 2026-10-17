//! The selected website and its account, and the `MODULE` object over them.

use std::collections::HashMap;

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::model::{AccountStatus, OptionValue, SiteDeclaration};
use crate::runtime::bind::{
    bool_field, borrow_state, ignore_unknown_fields, int_field, text_field,
};
use crate::runtime::convert::lua_int;
use crate::upstream::declaration::StorageObject;
use crate::upstream::http::{add_server_cookies, clear_cookies};

/// The website a session scrapes, kept in the runtime as `MODULE` exposes it.
pub(crate) struct SiteRecord {
    pub(crate) declaration: SiteDeclaration,
    /// The value of each setting: the declared default until a caller sets it.
    pub(crate) options: HashMap<String, OptionValue>,
    /// Which listing a site with several walks next. Modules see it as
    /// `MODULE.CurrentDirectoryIndex`.
    pub(crate) listing_index: i64,
}

impl SiteRecord {
    pub(crate) fn new(declaration: SiteDeclaration) -> Self {
        let options = declaration
            .options
            .iter()
            .filter_map(|o| Some((o.name.clone(), o.default.clone()?)))
            .collect();
        Self {
            declaration,
            options,
            listing_index: 0,
        }
    }
}

impl AccountStatus {
    /// Every state, in the order upstream numbers them.
    pub(crate) const ALL: [Self; 4] = [Self::Unknown, Self::Checking, Self::Valid, Self::Invalid];

    /// Returns the number upstream gives this state.
    pub(crate) fn code(self) -> i64 {
        match self {
            Self::Unknown => -1,
            Self::Checking => 0,
            Self::Valid => 1,
            Self::Invalid => 2,
        }
    }

    fn from_code(code: i64) -> Self {
        Self::ALL
            .into_iter()
            .find(|s| s.code() == code)
            .unwrap_or_default()
    }
}

/// The credentials a module sees as `MODULE.Account`, kept in the runtime.
#[derive(Clone, Debug, Default)]
pub(crate) struct AccountRecord {
    /// Whether credentials were supplied. Login handlers check this first.
    pub(crate) enabled: bool,
    pub(crate) username: String,
    pub(crate) password: String,
    /// Cookies the module saved for the session.
    pub(crate) cookies: String,
    pub(crate) status: AccountStatus,
}

/// `MODULE`. It holds nothing itself; every access reaches [`SiteRecord`].
pub(crate) struct ModuleObject;

impl UserData for ModuleObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "ID", |s: &mut SiteRecord| &mut s.declaration.id);
        text_field(fields, "Name", |s: &mut SiteRecord| &mut s.declaration.name);
        text_field(fields, "RootURL", |s: &mut SiteRecord| {
            &mut s.declaration.root_url
        });
        text_field(fields, "Category", |s: &mut SiteRecord| {
            &mut s.declaration.category
        });
        bool_field(fields, "AccountSupport", |s: &mut SiteRecord| {
            &mut s.declaration.takes_login
        });
        int_field(fields, "CurrentDirectoryIndex", |s: &mut SiteRecord| {
            &mut s.listing_index
        });
        fields.add_field_function_get("TotalDirectory", |lua, _: AnyUserData| {
            Ok(i64::from(
                borrow_state::<SiteRecord>(lua)?.declaration.listing_count,
            ))
        });
        fields.add_field_function_set("TotalDirectory", |lua, _: AnyUserData, value: Value| {
            let count = lua_int(lua, &value).clamp(1, i64::from(u32::MAX));
            borrow_state::<SiteRecord>(lua)?.declaration.listing_count =
                u32::try_from(count).unwrap_or(1);
            Ok(())
        });
        fields.add_field_function_get("Storage", |_, _: AnyUserData| Ok(StorageObject));
        fields.add_field_function_get("Account", |_, _: AnyUserData| Ok(AccountObject));
        fields.add_field_function_get("ActiveConnectionCount", |_, _: AnyUserData| Ok(1));
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_function("GetOption", |lua, name: String| get_option(lua, &name));
        // A few modules manage cookies through MODULE rather than HTTP.
        methods.add_function("ClearCookies", clear_cookies);
        methods.add_function("RemoveCookies", clear_cookies);
        methods.add_function("AddServerCookies", add_server_cookies);
        ignore_unknown_fields(methods);
    }
}

/// `MODULE.GetOption(name)`: the setting's value, or `nil` if it has none.
fn get_option(lua: &Lua, name: &str) -> mlua::Result<Value> {
    let site = borrow_state::<SiteRecord>(lua)?;
    site.options
        .get(name)
        .map_or(Ok(Value::Nil), |value| value.to_lua_value(lua))
}

/// `MODULE.Account`. It holds nothing itself; every access reaches [`AccountRecord`].
struct AccountObject;

impl UserData for AccountObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        bool_field(fields, "Enabled", |a: &mut AccountRecord| &mut a.enabled);
        text_field(fields, "Username", |a: &mut AccountRecord| &mut a.username);
        text_field(fields, "Password", |a: &mut AccountRecord| &mut a.password);
        text_field(fields, "Cookies", |a: &mut AccountRecord| &mut a.cookies);
        fields.add_field_function_get("Status", |lua, _: AnyUserData| {
            Ok(borrow_state::<AccountRecord>(lua)?.status.code())
        });
        fields.add_field_function_set("Status", |lua, _: AnyUserData, value: Value| {
            borrow_state::<AccountRecord>(lua)?.status =
                AccountStatus::from_code(lua_int(lua, &value));
            Ok(())
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        ignore_unknown_fields(methods);
    }
}
