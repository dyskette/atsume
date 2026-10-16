use std::path::PathBuf;

use tempfile::TempDir;

use super::*;
use crate::testing::{Checkout, open_module};

fn eval(runner: &Runner, expression: &str) -> String {
    runner.eval(expression)
}

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
        m.Storage['mode'] = 2
    end
end
GetInfo = shared.GetInfo
function Fail() error('broken on purpose') end";

fn example() -> Checkout {
    let template = "local _M = {}\nfunction _M.GetInfo() return 'template' end\nreturn _M";
    let module = format!("{BOM}{EXAMPLE}");
    Checkout::new(&[
        ("templates/Shared.lua", template),
        ("modules/Example.lua", &module),
    ])
}

fn option(
    kind: OptionKind,
    name: &str,
    caption: &str,
    default: Option<OptionValue>,
) -> ModuleOption {
    ModuleOption {
        kind,
        name: name.into(),
        caption: caption.into(),
        default,
        items: Vec::new(),
    }
}

#[test]
fn open_reads_the_declaration() {
    let checkout = example();
    let runner = checkout
        .open(
            "modules/Example.lua",
            Some("example"),
            Some("https://two.example"),
        )
        .unwrap();
    assert_eq!(runner.sites().len(), 2);

    let combo = |name: &str, caption: &str, items: &[&str], default| ModuleOption {
        items: items.iter().map(|&i| i.to_owned()).collect(),
        ..option(
            OptionKind::ComboBox,
            name,
            caption,
            Some(OptionValue::Int(default)),
        )
    };
    let want = Module {
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
        directories: 3,
        file: checkout.file("modules/Example.lua"),
    };
    assert_eq!(runner.module(), want);
    assert_eq!(eval(&runner, "MODULE.Storage['mode']"), "2");
}

#[test]
fn handlers_reach_templates_through_require() {
    let runner = example().open("modules/Example.lua", None, None).unwrap();
    assert_eq!(eval(&runner, "GetInfo()"), "template");
    let result = runner.call(Event::GetInfo).unwrap();
    assert_eq!(
        runner.lua().coerce_string(result).unwrap().unwrap(),
        "template"
    );
}

#[test]
fn a_failing_call_names_the_handler() {
    let runner = example().open("modules/Example.lua", None, None).unwrap();
    let unhandled = runner.call(Event::Login).unwrap_err().to_string();
    assert_eq!(unhandled, "module Example has no OnLogin handler");

    let undefined = runner.call(Event::GetPageNumber).unwrap_err().to_string();
    assert_eq!(
        undefined,
        r#"module Example declares OnGetPageNumber="Missing" but defines no such function"#
    );

    runner.set_handler(Event::CheckSite, "Fail");
    let failed = runner.call(Event::CheckSite).unwrap_err().to_string();
    assert!(failed.starts_with("Example/Fail: "), "{failed}");
    assert!(failed.contains("broken on purpose"), "{failed}");
}

#[test]
fn an_undeclared_site_is_an_error() {
    let Err(err) = example().open("modules/Example.lua", Some("Nope"), None) else {
        panic!("opened a site the file does not declare");
    };
    assert!(
        err.to_string()
            .contains(r#"declares no website named "Nope""#),
        "{err}"
    );
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
end";

#[test]
fn each_site_in_a_file_keeps_its_own_declaration() {
    let checkout = Checkout::new(&[("modules/Two.lua", TWO_SITES)]);
    let first = checkout.open("modules/Two.lua", None, None).unwrap();
    let names: Vec<_> = first.sites().iter().map(|s| s.name.as_str()).collect();
    assert_eq!(names, ["Open Site", "Gated Site"]);
    assert_eq!(first.module().name, "Open Site");
    assert_eq!(first.options().len(), 1);
    assert!(!first.has_handler(Event::Login));

    let gated = checkout
        .open("modules/Two.lua", Some("Gated Site"), None)
        .unwrap();
    assert_eq!(gated.module().root_url, "https://gated.example");
    assert_eq!(gated.options()[0].name, "b");
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
        checkout
            .open("modules/Doors.lua", Some("Many Doors"), url)
            .unwrap()
            .module()
            .root_url
    };
    assert_eq!(root(None), "https://one.example");
    assert_eq!(root(Some("https://two.example")), "https://two.example");
    // An address no mirror declares is one the reader typed for a moved site.
    assert_eq!(root(Some("https://moved.example")), "https://moved.example");
}

#[test]
fn init_may_return_its_declaration_instead() {
    let runner = open_module(
        "local m = NewWebsiteModule(); m.Name = 'Returned'; return m",
        "",
    );
    assert_eq!(runner.module().name, "Returned");

    let source = "function Init() return {Name = 'Bare'} end";
    let checkout = Checkout::new(&[("modules/Bare.lua", source)]);
    assert_eq!(
        checkout
            .open("modules/Bare.lua", None, None)
            .unwrap()
            .module()
            .name,
        "Bare"
    );
}

#[test]
fn a_file_that_declares_nothing_is_an_error() {
    let checkout = Checkout::new(&[("modules/Empty.lua", "function Init() end")]);
    let Err(err) = checkout.open("modules/Empty.lua", None, None) else {
        panic!("opened a file that declares no website");
    };
    assert!(err.kind() == ErrorKind::Load, "{err}");
}

#[test]
fn load_and_init_errors_name_the_file() {
    let checkout = Checkout::new(&[
        ("modules/Syntax.lua", "function Init("),
        ("modules/Raises.lua", "function Init() error('no') end"),
    ]);
    let load = checkout
        .open("modules/Syntax.lua", None, None)
        .err()
        .unwrap();
    assert!(
        load.kind() == ErrorKind::Load && load.to_string().starts_with("load "),
        "{load}"
    );
    let init = checkout
        .open("modules/Raises.lua", None, None)
        .err()
        .unwrap();
    assert!(
        init.kind() == ErrorKind::Load && init.to_string().starts_with("Init "),
        "{init}"
    );
}

#[test]
fn fmd_env_points_into_the_checkout() {
    let checkout = Checkout::new(&[("modules/Env.lua", "function Init() NewWebsiteModule() end")]);
    let runner = checkout.open("modules/Env.lua", None, None).unwrap();
    let dir = eval(&runner, "require('fmd.env').LuaDirectory");
    assert!(dir.ends_with(std::path::MAIN_SEPARATOR), "{dir}");
    // What modules do with it: concatenate a path onto it.
    assert!(Path::new(&format!("{dir}modules")).is_dir(), "{dir}");
    assert_eq!(eval(&runner, "require('fmd.env').SelectedLanguage"), "en");
}

#[test]
fn lua_5_4_allows_assigning_to_a_for_loop_variable() {
    // Lua 5.5 rejects this; several upstream modules do it.
    let runner = open_module("m = NewWebsiteModule()", "");
    assert_eq!(
        eval(
            &runner,
            "(function() local s = 0 for i = 1, 3 do i = i * 2; s = s + i end return s end)()"
        ),
        "12"
    );
}

#[test]
fn module_exposes_the_selected_site() {
    let runner = open_module(
        "local m = NewWebsiteModule(); m.Name = 'Site'; m.TotalDirectory = 2.7\n\
         m.AddOptionSpinEdit('n', 'N', 5); m.AddOptionCheckBox('c', 'C', false)\n\
         m.AddOptionEdit('t', 'T', 'x'); m.Storage.seed = 'from init'",
        "",
    );
    assert_eq!(
        eval(&runner, "MODULE.Name .. MODULE.TotalDirectory"),
        "Site2"
    );
    assert_eq!(
        eval(
            &runner,
            "MODULE.Storage.seed .. '|' .. MODULE.Storage.missing"
        ),
        "from init|"
    );
    assert_eq!(eval(&runner, "tostring(MODULE.NoSuchField)"), "nil");
    assert_eq!(eval(&runner, "MODULE.GetOption('n') + 1"), "6");

    runner.set_option_text("n", "7");
    runner.set_option_text("c", "true");
    runner.set_option_text("t", "42");
    assert_eq!(eval(&runner, "MODULE.GetOption('n') + 1"), "8");
    assert_eq!(eval(&runner, "tostring(MODULE.GetOption('c'))"), "true");
    assert_eq!(eval(&runner, "type(MODULE.GetOption('t'))"), "string");

    runner.set_directory_index(3);
    assert_eq!(eval(&runner, "MODULE.CurrentDirectoryIndex"), "3");
    assert_eq!(runner.directories(), 2);
}

const LOGIN: &str = "
function Login()
    if MODULE.Account.Username == 'good' then
        MODULE.Account.Status = asValid
    else
        MODULE.Account.Status = asInvalid
    end
    return true
end";

#[test]
fn login_reports_what_the_module_recorded() {
    let runner = open_module(
        "local m = NewWebsiteModule(); m.Name = 'L'; m.OnLogin = 'Login'",
        LOGIN,
    );
    assert_eq!(runner.login().unwrap_err().kind(), ErrorKind::Unsupported);
    assert_eq!(eval(&runner, "MODULE.Account.Status"), "-1");

    runner.set_account("good", "pw");
    assert!(runner.login().unwrap());
    assert_eq!(runner.account_status(), AccountStatus::Valid);

    runner.set_account("bad", "pw");
    assert_eq!(runner.login().unwrap_err().kind(), ErrorKind::LoginRejected);
}

#[test]
fn directory_page_count_reads_pagenumber() {
    let init = "local m = NewWebsiteModule(); m.Name = 'D'; m.OnGetDirectoryPageNumber = 'Pages'";
    let runner = open_module(
        init,
        "function Pages() PAGENUMBER = 12.0; return no_error end",
    );
    assert_eq!(runner.directory_page_count().unwrap(), 12);
    let without_handler = open_module("NewWebsiteModule()", "");
    assert_eq!(without_handler.directory_page_count().unwrap(), 1);
}

#[test]
fn after_image_saved_may_edit_only_that_page() {
    let pages = TempDir::new().unwrap();
    let page = pages.path().join("0001.jpg");
    std::fs::write(&page, "ORIGINAL").unwrap();
    let init = "local m = NewWebsiteModule(); m.Name = 'W'; m.OnAfterImageSaved = 'Edit'";
    let rest =
        "function Edit() local f = io.open(FILENAME, 'wb'); f:write('EDITED'); f:close() end";
    let runner = open_module(init, rest);

    runner.after_image_saved(&page).unwrap();
    assert_eq!(std::fs::read_to_string(&page).unwrap(), "EDITED");
    // Once the hook is over, the page is out of reach again.
    let opened = eval(&runner, "tostring(io.open(FILENAME, 'w'))");
    assert_eq!(opened, "nil");
}

/// A module whose handlers loop, with `cancel()` cancelling its own scrape.
fn looping_runner() -> (Runner, CancelToken) {
    let init = "local m = NewWebsiteModule(); m.Name = 'Loop'\n\
        m.OnGetInfo = 'Spin'; m.OnCheckSite = 'Fine'; m.OnTaskStart = 'InCoroutine'\n\
        m.OnGetPageNumber = 'CancelThenSpin'; m.OnLogin = 'Swallow'; m.OnGetImageURL = 'Stubborn'";
    let rest = "
        function Spin() while true do end end
        function Fine() RAN = true; return 'fine' end
        function InCoroutine() coroutine.wrap(function() while true do end end)() end
        function CancelThenSpin() cancel(); while true do end end
        function Swallow() cancel(); pcall(function() while true do end end); return 'looks fine' end
        function Stubborn() cancel(); while true do pcall(function() for i = 1, 50 do end end) end end";
    let cancel = CancelToken::new();
    let source = format!("function Init()\n{init}\nend\n{rest}");
    let checkout = Checkout::new(&[("modules/Loop.lua", &source)]);
    let host = Host::new(checkout.dir.path().join("lua"));
    let runner = host
        .open(
            &checkout.file("modules/Loop.lua"),
            None,
            None,
            cancel.clone(),
        )
        .unwrap();
    let token = cancel.clone();
    let cancel_fn = runner.lua().create_function(move |_, ()| {
        token.cancel();
        Ok(())
    });
    runner
        .lua()
        .globals()
        .set("cancel", cancel_fn.unwrap())
        .unwrap();
    (runner, cancel)
}

#[test]
fn a_runaway_loop_stops_at_the_cpu_limit() {
    let (runner, _) = looping_runner();
    runner.set_cpu_limit(1_000_000);
    let err = runner.call(Event::GetInfo).unwrap_err();
    assert!(err.kind() == ErrorKind::CpuLimit, "{err}");
    assert!(err.to_string().contains("ran too long"), "{err}");
    // The limit is per call, so the runner is still usable.
    let fine = runner.call(Event::CheckSite).unwrap();
    assert_eq!(runner.lua().coerce_string(fine).unwrap().unwrap(), "fine");
    assert!(runner.peak_instructions() >= 1_000_000);
}

#[test]
fn the_cpu_limit_reaches_into_coroutines() {
    let (runner, _) = looping_runner();
    runner.set_cpu_limit(1_000_000);
    let err = runner.call(Event::TaskStart).unwrap_err();
    assert!(err.kind() == ErrorKind::CpuLimit, "{err}");
}

#[test]
fn cancelling_stops_the_running_call_and_every_later_one() {
    let (runner, cancel) = looping_runner();
    let running = runner.call(Event::GetPageNumber).unwrap_err();
    assert!(running.kind() == ErrorKind::Cancelled, "{running}");
    let later = runner.call(Event::CheckSite).unwrap_err();
    assert!(later.kind() == ErrorKind::Cancelled, "{later}");
    // Read from Rust: a cancelled runner runs no more Lua, not even this.
    assert!(runner.lua().globals().get::<Value>("RAN").unwrap().is_nil());

    let host = Host::new(PathBuf::from("unused"));
    let opening = host
        .open(Path::new("unused.lua"), None, None, cancel)
        .err()
        .unwrap();
    assert!(opening.kind() == ErrorKind::Cancelled, "{opening}");
}

#[test]
fn a_caught_termination_is_still_a_cancellation() {
    let (runner, _) = looping_runner();
    let err = runner.call(Event::Login).unwrap_err();
    assert!(err.kind() == ErrorKind::Cancelled, "{err}");
}

#[test]
fn a_loop_that_keeps_catching_the_termination_still_stops() {
    let (runner, _) = looping_runner();
    let err = runner.call(Event::GetImageUrl).unwrap_err();
    assert!(err.kind() == ErrorKind::Cancelled, "{err}");
}
