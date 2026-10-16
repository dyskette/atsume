//! What a module declares about a website from its `Init()`.
//!
//! `Init()` calls `NewWebsiteModule()` once per website and fills in the
//! table it returns: the site's name and address, the Lua functions handling
//! each event, and the settings it offers. [`Module`] is that declaration,
//! read back into Rust.

use std::cell::RefCell;
use std::collections::HashMap;
use std::path::{Path, PathBuf};
use std::rc::Rc;

use mlua::{Function, Lua, MetaMethod, Table, UserData, UserDataMethods, Value, Variadic};

use crate::object::truthy;
use crate::sandbox::text_of;

/// The widget a module asks for when declaring a setting.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum OptionKind {
    /// A boolean switch.
    CheckBox,
    /// A number.
    SpinEdit,
    /// A choice from a list; the value is the chosen item's index.
    ComboBox,
    /// Free text.
    EditBox,
}

/// A setting's value, kept free of Lua types so callers never depend on them.
#[derive(Clone, Debug, PartialEq)]
pub enum OptionValue {
    /// A checkbox state.
    Bool(bool),
    /// A whole number. A whole float such as `1.0` is read as this.
    Int(i64),
    /// A fractional number.
    Float(f64),
    /// Text.
    Text(String),
}

impl OptionValue {
    /// Reads a Lua value, or `None` for `nil` and for values no setting holds.
    pub(crate) fn from_lua(value: &Value) -> Option<Self> {
        match value {
            Value::Boolean(b) => Some(Self::Bool(*b)),
            Value::Integer(n) => Some(Self::Int(*n)),
            Value::Number(f) => Some(whole_number(*f).map_or(Self::Float(*f), Self::Int)),
            Value::String(s) => Some(Self::Text(s.to_string_lossy())),
            _ => None,
        }
    }

    pub(crate) fn to_lua(&self, lua: &Lua) -> mlua::Result<Value> {
        Ok(match self {
            Self::Bool(b) => Value::Boolean(*b),
            Self::Int(n) => Value::Integer(*n),
            Self::Float(f) => Value::Number(*f),
            Self::Text(s) => Value::String(lua.create_string(s)?),
        })
    }
}

/// Returns `f` as an integer if it is whole and exactly representable.
fn whole_number(f: f64) -> Option<i64> {
    const EXACT: f64 = 9_007_199_254_740_992.0; // 2^53
    if f.fract() != 0.0 || f.abs() >= EXACT {
        return None;
    }
    #[expect(clippy::cast_possible_truncation, reason = "f is whole and within i64")]
    let n = f as i64;
    Some(n)
}

/// One setting a module declares. The settings page is generated from these.
#[derive(Clone, Debug, PartialEq)]
pub struct ModuleOption {
    /// The widget to show.
    pub kind: OptionKind,
    /// The key the module reads it back by, through `MODULE.GetOption`.
    pub name: String,
    /// The label to show.
    pub caption: String,
    /// The value before the reader changes it, if the module gave one.
    pub default: Option<OptionValue>,
    /// The choices of a [`OptionKind::ComboBox`], and empty otherwise.
    pub items: Vec<String>,
}

/// A website as a module declared it in `Init()`.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct Module {
    /// The upstream identifier.
    pub id: String,
    /// The name shown to readers. Mirrors of one site share it.
    pub name: String,
    /// The address the module builds its requests from.
    pub root_url: String,
    /// The language or kind of site, as upstream groups them.
    pub category: String,
    /// Maps each event the module handles to the Lua function handling it.
    pub handlers: HashMap<Event, String>,
    /// The settings the site offers.
    pub options: Vec<ModuleOption>,
    /// Whether the site takes a login.
    pub account_support: bool,
    /// Whether the site lists its directory in a stable order.
    pub sorted_list: bool,
    /// How many separate listings the directory is split into; at least one.
    pub directories: u32,
    /// The file the module was loaded from.
    pub file: PathBuf,
}

impl Module {
    /// Returns the Lua function name handling `event`, if the module set one.
    #[must_use]
    pub fn handler(&self, event: Event) -> Option<&str> {
        self.handlers.get(&event).map(String::as_str)
    }
}

/// An event a module may handle, named after the slot it assigns in `Init()`.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Hash)]
pub enum Event {
    /// Lists one page of the site's directory.
    GetNameAndLink,
    /// Reads a series' details and chapter list.
    GetInfo,
    /// Lists a chapter's pages.
    GetPageNumber,
    /// Counts the pages of the site's directory.
    GetDirectoryPageNumber,
    /// Resolves a page to its image address.
    GetImageUrl,
    /// Prepares the request for a page image, usually by setting a `Referer`.
    BeforeDownloadImage,
    /// Fetches a page image itself, possibly transforming it.
    DownloadImage,
    /// Edits a saved page image in place.
    AfterImageSaved,
    /// Saves a page image itself.
    SaveImage,
    /// Logs in with the configured account.
    Login,
    /// Prepares a download task.
    TaskStart,
    /// Checks that the site is reachable.
    CheckSite,
    /// Reports the account's state.
    AccountState,
    /// Prepares a directory update.
    BeforeUpdateList,
    /// Finishes a directory update.
    AfterUpdateList,
}

impl Event {
    /// Every event, in upstream's order.
    pub const ALL: [Self; 15] = [
        Self::GetNameAndLink,
        Self::GetInfo,
        Self::GetPageNumber,
        Self::GetDirectoryPageNumber,
        Self::GetImageUrl,
        Self::BeforeDownloadImage,
        Self::DownloadImage,
        Self::AfterImageSaved,
        Self::SaveImage,
        Self::Login,
        Self::TaskStart,
        Self::CheckSite,
        Self::AccountState,
        Self::BeforeUpdateList,
        Self::AfterUpdateList,
    ];

    /// Returns the declaration field a module assigns the handler to.
    #[must_use]
    pub fn slot(self) -> &'static str {
        match self {
            Self::GetNameAndLink => "OnGetNameAndLink",
            Self::GetInfo => "OnGetInfo",
            Self::GetPageNumber => "OnGetPageNumber",
            Self::GetDirectoryPageNumber => "OnGetDirectoryPageNumber",
            Self::GetImageUrl => "OnGetImageURL",
            Self::BeforeDownloadImage => "OnBeforeDownloadImage",
            Self::DownloadImage => "OnDownloadImage",
            Self::AfterImageSaved => "OnAfterImageSaved",
            Self::SaveImage => "OnSaveImage",
            Self::Login => "OnLogin",
            Self::TaskStart => "OnTaskStart",
            Self::CheckSite => "OnCheckSite",
            Self::AccountState => "OnAccountState",
            Self::BeforeUpdateList => "OnBeforeUpdateList",
            Self::AfterUpdateList => "OnAfterUpdateList",
        }
    }
}

impl std::fmt::Display for Event {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(self.slot())
    }
}

/// The settings one declaration collects while `Init()` runs.
pub(crate) type Options = Rc<RefCell<Vec<ModuleOption>>>;

/// Builds the table `NewWebsiteModule()` returns.
///
/// It holds the option setters and `Storage`. Modules call the setters with a
/// dot, so the first argument is the option's name, not the table.
pub(crate) fn declaration_table(
    lua: &Lua,
    options: &Options,
    storage: Value,
) -> mlua::Result<Table> {
    let table = lua.create_table()?;
    table.set("Storage", storage)?;
    for (name, kind) in [
        ("AddOptionCheckBox", OptionKind::CheckBox),
        ("AddOptionSpinEdit", OptionKind::SpinEdit),
        ("AddOptionComboBox", OptionKind::ComboBox),
        ("AddOptionEdit", OptionKind::EditBox),
        ("AddOptionEditBox", OptionKind::EditBox),
    ] {
        table.set(name, option_setter(lua, kind, Rc::clone(options))?)?;
    }
    Ok(table)
}

fn option_setter(lua: &Lua, kind: OptionKind, options: Options) -> mlua::Result<Function> {
    lua.create_function(move |lua, args: Variadic<Value>| {
        let option = parse_option(lua, kind, &args)?;
        options.borrow_mut().push(option);
        Ok(())
    })
}

/// Reads a setter's arguments: name, caption, then the default, with a combo
/// box's items between the caption and the default.
fn parse_option(lua: &Lua, kind: OptionKind, args: &[Value]) -> mlua::Result<ModuleOption> {
    let arg = |i: usize| args.get(i).unwrap_or(&Value::Nil);
    let name = text_of(lua, arg(0)).ok_or_else(|| {
        mlua::Error::runtime(format!(
            "bad argument #1 (string expected, got {})",
            arg(0).type_name()
        ))
    })?;
    let (items, default) = match kind {
        OptionKind::ComboBox => (combo_items(lua, arg(2)), arg(3)),
        _ => (Vec::new(), arg(2)),
    };
    Ok(ModuleOption {
        kind,
        name,
        caption: text_of(lua, arg(1)).unwrap_or_default(),
        default: OptionValue::from_lua(default),
        items,
    })
}

/// Reads a combo box's choices: one per line of a string, as nearly every
/// module passes them, or the items of a table.
fn combo_items(lua: &Lua, value: &Value) -> Vec<String> {
    match value {
        Value::String(s) => split_lines(&s.to_string_lossy()),
        Value::Table(t) => t
            .sequence_values::<Value>()
            .map(|item| item.ok().and_then(|v| text_of(lua, &v)).unwrap_or_default())
            .collect(),
        _ => Vec::new(),
    }
}

/// Splits text into lines, accepting both LF and CRLF line endings.
pub(crate) fn split_lines(text: &str) -> Vec<String> {
    text.lines().map(str::to_owned).collect()
}

/// Reads a declaration table back into a [`Module`].
///
/// Text fields accept a number, as Lua would; flags take Lua truthiness.
pub(crate) fn read_declaration(
    lua: &Lua,
    table: &Table,
    options: Vec<ModuleOption>,
    file: &Path,
) -> mlua::Result<Module> {
    let text = |key: &str| -> mlua::Result<String> {
        Ok(text_of(lua, &table.get::<Value>(key)?).unwrap_or_default())
    };
    let mut handlers = HashMap::new();
    for event in Event::ALL {
        let function = text(event.slot())?;
        if !function.is_empty() {
            handlers.insert(event, function);
        }
    }
    let directories = lua.coerce_number(table.get("TotalDirectory")?)?;
    Ok(Module {
        id: text("ID")?,
        name: text("Name")?,
        root_url: text("RootURL")?,
        category: text("Category")?,
        handlers,
        options,
        account_support: truthy(&table.get("AccountSupport")?),
        sorted_list: truthy(&table.get("SortedList")?),
        directories: directory_count(directories),
        file: file.to_path_buf(),
    })
}

/// Reads a declared `TotalDirectory`, where anything below one means one.
fn directory_count(declared: Option<f64>) -> u32 {
    declared
        .and_then(|n| whole_number(n.trunc()))
        .and_then(|n| u32::try_from(n).ok())
        .unwrap_or(1)
        .max(1)
}

/// The per-file key/value scratchpad modules reach as `Storage`.
///
/// Every declaration in a file and `MODULE.Storage` share one, because some
/// modules seed it from `Init()` and read it back from their handlers.
pub(crate) type Storage = Rc<RefCell<HashMap<String, String>>>;

/// The Lua face of a [`Storage`]: `Storage['key']`, where a missing key reads
/// as `""`, because templates compare it with the empty string.
pub(crate) struct StorageHandle(pub(crate) Storage);

impl UserData for StorageHandle {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, storage, key: Value| {
            let key = storage_text(lua, &key, 2)?;
            Ok(storage.0.borrow().get(&key).cloned().unwrap_or_default())
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, storage, (key, value): (Value, Value)| {
                let key = storage_text(lua, &key, 2)?;
                let value = storage_text(lua, &value, 3)?;
                storage.0.borrow_mut().insert(key, value);
                Ok(())
            },
        );
    }
}

fn storage_text(lua: &Lua, value: &Value, position: usize) -> mlua::Result<String> {
    text_of(lua, value).ok_or_else(|| {
        mlua::Error::runtime(format!(
            "bad argument #{position} (string expected, got {})",
            value.type_name()
        ))
    })
}
