//! The selected website and its account, and the `MODULE` object over them.

use std::collections::HashMap;

use mlua::{AnyUserData, Lua, UserData, UserDataFields, UserDataMethods, Value};

use crate::model::{AccountStatus, Module, OptionValue};
use crate::runtime::bind::{bool_field, int_field, lenient, state, text_field};
use crate::runtime::convert::to_int;
use crate::upstream::declaration::StorageObject;
use crate::upstream::http::{add_server_cookies, clear_cookies};

/// The website a runner scrapes, kept in the runtime as `MODULE` exposes it.
pub(crate) struct SiteState {
    pub(crate) module: Module,
    /// The value of each setting: the declared default until a caller sets it.
    pub(crate) options: HashMap<String, OptionValue>,
    /// Which listing a site with several walks next.
    pub(crate) directory_index: i64,
}

impl SiteState {
    pub(crate) fn new(module: Module) -> Self {
        let options = module
            .options
            .iter()
            .filter_map(|o| Some((o.name.clone(), o.default.clone()?)))
            .collect();
        Self {
            module,
            options,
            directory_index: 0,
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
pub(crate) struct Account {
    /// Whether credentials were supplied. Login handlers check this first.
    pub(crate) enabled: bool,
    pub(crate) username: String,
    pub(crate) password: String,
    /// Cookies the module saved for the session.
    pub(crate) cookies: String,
    pub(crate) status: AccountStatus,
}

/// `MODULE`. It holds nothing itself; every access reaches [`SiteState`].
pub(crate) struct ModuleObject;

impl UserData for ModuleObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        text_field(fields, "ID", |s: &mut SiteState| &mut s.module.id);
        text_field(fields, "Name", |s: &mut SiteState| &mut s.module.name);
        text_field(fields, "RootURL", |s: &mut SiteState| {
            &mut s.module.root_url
        });
        text_field(fields, "Category", |s: &mut SiteState| {
            &mut s.module.category
        });
        bool_field(fields, "AccountSupport", |s: &mut SiteState| {
            &mut s.module.account_support
        });
        int_field(fields, "CurrentDirectoryIndex", |s: &mut SiteState| {
            &mut s.directory_index
        });
        fields.add_field_function_get("TotalDirectory", |lua, _: AnyUserData| {
            Ok(i64::from(state::<SiteState>(lua)?.module.directories))
        });
        fields.add_field_function_set("TotalDirectory", |lua, _: AnyUserData, value: Value| {
            let count = to_int(lua, &value).clamp(1, i64::from(u32::MAX));
            state::<SiteState>(lua)?.module.directories = u32::try_from(count).unwrap_or(1);
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
        lenient(methods);
    }
}

/// `MODULE.GetOption(name)`: the setting's value, or `nil` if it has none.
fn get_option(lua: &Lua, name: &str) -> mlua::Result<Value> {
    let site = state::<SiteState>(lua)?;
    site.options
        .get(name)
        .map_or(Ok(Value::Nil), |value| value.to_lua(lua))
}

/// `MODULE.Account`. It holds nothing itself; every access reaches [`Account`].
struct AccountObject;

impl UserData for AccountObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        bool_field(fields, "Enabled", |a: &mut Account| &mut a.enabled);
        text_field(fields, "Username", |a: &mut Account| &mut a.username);
        text_field(fields, "Password", |a: &mut Account| &mut a.password);
        text_field(fields, "Cookies", |a: &mut Account| &mut a.cookies);
        fields.add_field_function_get("Status", |lua, _: AnyUserData| {
            Ok(state::<Account>(lua)?.status.code())
        });
        fields.add_field_function_set("Status", |lua, _: AnyUserData, value: Value| {
            state::<Account>(lua)?.status = AccountStatus::from_code(to_int(lua, &value));
            Ok(())
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        lenient(methods);
    }
}
