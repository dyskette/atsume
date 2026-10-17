//! What a module declares about a website from its `Init()`.
//!
//! `Init()` calls `NewWebsiteModule()` once per website and fills in the
//! table it returns: the site's name and address, the Lua functions handling
//! each event, and the settings it offers. [`Module`] is that declaration,
//! read back into Rust.

use std::collections::HashMap;
use std::path::Path;

use mlua::{AnyUserData, Lua, MetaMethod, Table, UserData, UserDataMethods, Value, Variadic};

use crate::model::{Event, Module, ModuleOption, OptionKind, OptionValue};
use crate::runtime::bind::state;
use crate::runtime::convert::{string_arg, text_of, truthy};
use crate::string_list::split_lines;

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

/// The declarations `NewWebsiteModule()` handed out while a file runs, kept
/// in the runtime until the host reads them.
#[derive(Default)]
pub(crate) struct Declarations(Vec<Declaration>);

struct Declaration {
    table: Table,
    /// Each declaration collects its own options, so two sites in one file do
    /// not show each other's settings.
    options: Vec<ModuleOption>,
}

/// Defines `NewWebsiteModule`, which records each declaration it returns.
pub(crate) fn register_declarations(lua: &Lua) -> mlua::Result<()> {
    lua.set_app_data(Declarations::default());
    let new_website_module = lua.create_function(|lua, ()| {
        let index = state::<Declarations>(lua)?.0.len();
        let table = declaration_table(lua, index)?;
        let declaration = Declaration {
            table: table.clone(),
            options: Vec::new(),
        };
        state::<Declarations>(lua)?.0.push(declaration);
        Ok(table)
    })?;
    lua.globals().set("NewWebsiteModule", new_website_module)
}

/// Reads every declaration the file made into a [`Module`], in order, or
/// `None` if it made none.
///
/// Most modules fill the table and fall off the end of `Init()`; a few return
/// it instead, which is the same single declaration.
pub(crate) fn take_declarations(
    lua: &Lua,
    init_result: Value,
    file: &Path,
) -> mlua::Result<Option<Vec<Module>>> {
    let mut declarations = lua.remove_app_data::<Declarations>().unwrap_or_default().0;
    if let (Value::Table(table), true) = (init_result, declarations.is_empty()) {
        declarations.push(Declaration {
            table,
            options: Vec::new(),
        });
    }
    if declarations.is_empty() {
        return Ok(None);
    }
    let modules = declarations
        .into_iter()
        .map(|d| read_declaration(lua, &d.table, d.options, file))
        .collect::<mlua::Result<_>>()?;
    Ok(Some(modules))
}

/// Builds the table `NewWebsiteModule()` returns for declaration `index`.
///
/// It holds the option setters and `Storage`. Modules call the setters with a
/// dot, so the first argument is the option's name, not the table.
fn declaration_table(lua: &Lua, index: usize) -> mlua::Result<Table> {
    let table = lua.create_table()?;
    table.set("Storage", StorageObject)?;
    for (name, kind) in [
        ("AddOptionCheckBox", OptionKind::CheckBox),
        ("AddOptionSpinEdit", OptionKind::SpinEdit),
        ("AddOptionComboBox", OptionKind::ComboBox),
        ("AddOptionEdit", OptionKind::EditBox),
        ("AddOptionEditBox", OptionKind::EditBox),
    ] {
        let setter = lua.create_function(move |lua, args: Variadic<Value>| {
            let option = parse_option(lua, kind, &args)?;
            state::<Declarations>(lua)?.0[index].options.push(option);
            Ok(())
        })?;
        table.set(name, setter)?;
    }
    Ok(table)
}

/// Reads a setter's arguments: name, caption, then the default, with a combo
/// box's items between the caption and the default.
fn parse_option(lua: &Lua, kind: OptionKind, args: &[Value]) -> mlua::Result<ModuleOption> {
    let arg = |i: usize| args.get(i).unwrap_or(&Value::Nil);
    let name = string_arg(lua, arg(0), 1)?;
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

/// Reads a declaration table back into a [`Module`].
///
/// Text fields accept a number, as Lua would; flags take Lua truthiness.
fn read_declaration(
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

/// The per-file key/value scratchpad modules reach as `Storage`, kept in the
/// runtime.
///
/// Every declaration in a file and `MODULE.Storage` share one, because some
/// modules seed it from `Init()` and read it back from their handlers.
#[derive(Debug, Default)]
pub(crate) struct Storage(HashMap<String, String>);

/// `Storage['key']`, where a missing key reads as `""`, because templates
/// compare it with the empty string.
pub(crate) struct StorageObject;

impl UserData for StorageObject {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_function(MetaMethod::Index, |lua, (_, key): (AnyUserData, Value)| {
            let key = string_arg(lua, &key, 2)?;
            Ok(state::<Storage>(lua)?
                .0
                .get(&key)
                .cloned()
                .unwrap_or_default())
        });
        methods.add_meta_function(
            MetaMethod::NewIndex,
            |lua, (_, key, value): (AnyUserData, Value, Value)| {
                let key = string_arg(lua, &key, 2)?;
                let value = string_arg(lua, &value, 3)?;
                state::<Storage>(lua)?.0.insert(key, value);
                Ok(())
            },
        );
    }
}
