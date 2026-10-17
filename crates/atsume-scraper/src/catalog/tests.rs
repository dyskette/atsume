use std::collections::HashMap;

use super::*;
use crate::model::{Event, OptionKind, OptionValue, SiteOption};
use crate::testing::Checkout;

const BOM: &str = "\u{FEFF}";

const EXAMPLE: &str = r"
local shared = require 'templates.Shared'
function Init()
    for _, root in ipairs({'https://one.example', 'https://two.example'}) do
        local m = NewWebsiteModule()
        m.ID       = 'e1'
        m.Name     = 'Example'
        m.RootURL  = root
        m.Category = 'English'
        m.OnGetInfo = 'GetInfo'
        m.OnGetPageNumber = 'Missing'
        m.TotalDirectory = '3'
        m.AccountSupport = 1
        m.AddOptionCheckBox('paid', 'Show paid chapters', false)
        m.AddOptionSpinEdit('delay', 'Delay', 1.0)
        m.AddOptionComboBox('lang', 'Language', {'en', 'es'}, 1)
        m.AddOptionComboBox('svr', 'Server', 'Main\nSecondary\r\nCompress', 0)
        m.AddOptionEdit('token', 'Token')
    end
end
GetInfo = shared.GetInfo";

/// A checkout with one module, which starts with a byte-order mark as some
/// upstream modules do, and the template it requires.
fn example() -> Checkout {
    let template = "local _M = {}\nfunction _M.GetInfo() return 'template' end\nreturn _M";
    let module = format!("{BOM}{EXAMPLE}");
    Checkout::new(&[
        ("templates/Shared.lua", template),
        ("modules/Example.lua", &module),
    ])
}

fn option(kind: OptionKind, name: &str, caption: &str, default: Option<OptionValue>) -> SiteOption {
    SiteOption {
        kind,
        name: name.into(),
        caption: caption.into(),
        default,
        items: Vec::new(),
    }
}

fn combo(name: &str, caption: &str, items: &[&str], default: i64) -> SiteOption {
    SiteOption {
        items: items.iter().map(|&i| i.to_owned()).collect(),
        ..option(
            OptionKind::ComboBox,
            name,
            caption,
            Some(OptionValue::Int(default)),
        )
    }
}

#[test]
fn a_file_declares_its_websites() {
    let checkout = example();
    let sites = checkout.sites("modules/Example.lua").unwrap();
    let roots: Vec<_> = sites
        .iter()
        .map(|s| s.declaration().root_url.as_str())
        .collect();
    assert_eq!(roots, ["https://one.example", "https://two.example"]);

    let want = SiteDeclaration {
        id: "e1".into(),
        name: "Example".into(),
        root_url: "https://two.example".into(),
        category: "English".into(),
        handlers: HashMap::from([
            (Event::GetInfo, "GetInfo".into()),
            (Event::GetPageNumber, "Missing".into()),
        ]),
        options: vec![
            option(
                OptionKind::CheckBox,
                "paid",
                "Show paid chapters",
                Some(OptionValue::Bool(false)),
            ),
            option(
                OptionKind::SpinEdit,
                "delay",
                "Delay",
                Some(OptionValue::Int(1)),
            ),
            combo("lang", "Language", &["en", "es"], 1),
            combo("svr", "Server", &["Main", "Secondary", "Compress"], 0),
            option(OptionKind::EditBox, "token", "Token", None),
        ],
        account_support: true,
        sorted_list: false,
        listing_count: 3,
        file: checkout.file("modules/Example.lua"),
    };
    assert_eq!(sites[1].declaration(), &want);
}

#[test]
fn a_catalog_loads_every_module_and_records_failures() {
    let checkout = Checkout::new(&[
        (
            "modules/A.lua",
            "function Init() local m = NewWebsiteModule(); m.Name = 'A' end",
        ),
        ("modules/Broken.lua", "function Init("),
        ("modules/notes.txt", "not a module"),
    ]);
    let catalog = Catalog::load(checkout.lua_dir()).unwrap();
    let names: Vec<_> = catalog
        .sites()
        .iter()
        .map(|s| s.declaration().name.as_str())
        .collect();
    assert_eq!(names, ["A"]);
    assert_eq!(catalog.failures().len(), 1);
    assert_eq!(
        catalog.failures()[0].file,
        checkout.file("modules/Broken.lua")
    );
    assert_eq!(catalog.failures()[0].error.kind(), ErrorKind::Load);
    assert!(catalog.site("a", None).is_some());
    assert!(catalog.site("Nope", None).is_none());
}

#[test]
fn loading_the_catalog_reaches_no_network() {
    // Loading fails if the request gets through, and passes if it is refused.
    let source = "function Init() NewWebsiteModule().Name = 'Quiet'
        if HTTP.GET('https://example.com/') then error('reached the network') end end";
    let checkout = Checkout::new(&[("modules/Quiet.lua", source)]);
    let sites = checkout.sites("modules/Quiet.lua").unwrap();
    assert_eq!(sites[0].declaration().name, "Quiet");
}

const TWO_SITES: &str = r"
function Init()
    local open = NewWebsiteModule()
    open.Name = 'Open Site'
    open.RootURL = 'https://open.example'
    open.AddOptionCheckBox('a', 'A', true)
    local gated = NewWebsiteModule()
    gated.Name = 'Gated Site'
    gated.RootURL = 'https://gated.example'
    gated.OnLogin = 'Login'
    gated.AddOptionCheckBox('b', 'B', false)
end
function Login() return true end";

#[test]
fn each_site_in_a_file_keeps_its_own_declaration() {
    let checkout = Checkout::new(&[("modules/Two.lua", TWO_SITES)]);
    let sites = checkout.sites("modules/Two.lua").unwrap();
    let names: Vec<_> = sites
        .iter()
        .map(|s| s.declaration().name.as_str())
        .collect();
    assert_eq!(names, ["Open Site", "Gated Site"]);
    assert_eq!(sites[0].declaration().options.len(), 1);
    assert!(sites[0].declaration().handler(Event::Login).is_none());

    let gated = checkout
        .open("modules/Two.lua", Some("Gated Site"), None)
        .unwrap();
    assert_eq!(gated.declaration().root_url, "https://gated.example");
    assert_eq!(gated.declaration().options[0].name, "b");
    assert!(gated.has_handler(Event::Login));
}

#[test]
fn mirrors_share_a_name_and_differ_by_address() {
    let source = r"
function Init()
    for _, url in ipairs({'https://one.example', 'https://two.example'}) do
        local m = NewWebsiteModule()
        m.Name = 'Many Doors'
        m.RootURL = url
    end
end";
    let checkout = Checkout::new(&[("modules/Doors.lua", source)]);
    let root = |url: Option<&str>| {
        let session = checkout
            .open("modules/Doors.lua", Some("Many Doors"), url)
            .unwrap();
        session.declaration().root_url
    };
    assert_eq!(root(None), "https://one.example");
    assert_eq!(root(Some("https://two.example")), "https://two.example");
    // An address no mirror declares is one the reader typed for a moved site.
    assert_eq!(root(Some("https://moved.example")), "https://moved.example");
}

#[test]
fn init_may_return_its_declaration_instead() {
    let returned =
        "function Init() local m = NewWebsiteModule(); m.Name = 'Returned'; return m end";
    let bare = "function Init() return {Name = 'Bare'} end";
    let checkout = Checkout::new(&[
        ("modules/Returned.lua", returned),
        ("modules/Bare.lua", bare),
    ]);
    assert_eq!(
        checkout.sites("modules/Returned.lua").unwrap()[0]
            .declaration()
            .name,
        "Returned"
    );
    assert_eq!(
        checkout.sites("modules/Bare.lua").unwrap()[0]
            .declaration()
            .name,
        "Bare"
    );
}

#[test]
fn load_failures_name_the_file() {
    let checkout = Checkout::new(&[
        ("modules/Syntax.lua", "function Init("),
        ("modules/Raises.lua", "function Init() error('no') end"),
        ("modules/Empty.lua", "function Init() end"),
    ]);
    for (file, prefix) in [("Syntax", "load "), ("Raises", "Init "), ("Empty", "")] {
        let err = checkout.sites(&format!("modules/{file}.lua")).unwrap_err();
        assert_eq!(err.kind(), ErrorKind::Load, "{err}");
        assert!(err.to_string().starts_with(prefix), "{err}");
        assert!(err.to_string().contains(&format!("{file}.lua")), "{err}");
    }
}

#[test]
fn a_site_that_its_file_no_longer_declares_does_not_open() {
    let checkout = Checkout::new(&[("modules/Gone.lua", TWO_SITES)]);
    let gated = checkout
        .site("modules/Gone.lua", Some("Gated Site"), None)
        .unwrap();
    std::fs::write(
        checkout.file("modules/Gone.lua"),
        "function Init() NewWebsiteModule().Name = 'Other' end",
    )
    .unwrap();
    let err = gated
        .open(&SessionConfig::default(), CancelToken::new())
        .err()
        .unwrap();
    assert_eq!(err.kind(), ErrorKind::Load);
    assert!(err.to_string().contains("reload the catalog"), "{err}");
}
