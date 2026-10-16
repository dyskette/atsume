use std::path::Path;

use tempfile::TempDir;

use super::*;
use crate::testing::{Checkout, module_source, open_module};
use crate::{CancelToken, SessionConfig};

const FAILING: &str = "
local shared = require 'templates.Shared'
function Init()
    local m = NewWebsiteModule()
    m.Name = 'Example'
    m.OnGetInfo = 'GetInfo'
    m.OnGetPageNumber = 'Missing'
end
GetInfo = shared.GetInfo
function Fail() error('broken on purpose') end";

fn example() -> Session {
    let template = "local _M = {}\nfunction _M.GetInfo() return 'template' end\nreturn _M";
    let checkout = Checkout::new(&[
        ("templates/Shared.lua", template),
        ("modules/Example.lua", FAILING),
    ]);
    checkout.open("modules/Example.lua", None, None).unwrap()
}

#[test]
fn handlers_reach_templates_through_require() {
    let session = example();
    let result = session.call(Event::GetInfo).unwrap();
    assert_eq!(
        session.lua().coerce_string(result).unwrap().unwrap(),
        "template"
    );
}

#[test]
fn a_failing_call_names_the_handler() {
    let session = example();
    let unhandled = session.call(Event::Login).unwrap_err();
    assert_eq!(unhandled.kind(), ErrorKind::Unsupported);
    assert_eq!(
        unhandled.to_string(),
        "module Example has no OnLogin handler"
    );

    let undefined = session.call(Event::GetPageNumber).unwrap_err();
    assert_eq!(undefined.kind(), ErrorKind::Module);
    assert_eq!(
        undefined.to_string(),
        r#"module Example declares OnGetPageNumber="Missing" but defines no such function"#
    );

    session.set_handler(Event::CheckSite, "Fail");
    let failed = session.call(Event::CheckSite).unwrap_err().to_string();
    assert!(failed.starts_with("Example/Fail: "), "{failed}");
    assert!(failed.contains("broken on purpose"), "{failed}");
}

#[test]
fn fmd_env_points_into_the_checkout() {
    let checkout = Checkout::new(&[("modules/Env.lua", "function Init() NewWebsiteModule() end")]);
    let session = checkout.open("modules/Env.lua", None, None).unwrap();
    let dir = session.eval("require('fmd.env').LuaDirectory");
    assert!(dir.ends_with(std::path::MAIN_SEPARATOR), "{dir}");
    // What modules do with it: concatenate a path onto it.
    assert!(Path::new(&format!("{dir}modules")).is_dir(), "{dir}");
    assert_eq!(session.eval("require('fmd.env').SelectedLanguage"), "en");
}

#[test]
fn lua_5_4_allows_assigning_to_a_for_loop_variable() {
    // Lua 5.5 rejects this; several upstream modules do it.
    let session = open_module("NewWebsiteModule()", "");
    let source = "(function() local s = 0 for i = 1, 3 do i = i * 2; s = s + i end return s end)()";
    assert_eq!(session.eval(source), "12");
}

const SETTINGS: &str = "local m = NewWebsiteModule(); m.Name = 'Site'; m.TotalDirectory = 2.7
    m.AddOptionSpinEdit('n', 'N', 5); m.AddOptionCheckBox('c', 'C', false)
    m.AddOptionEdit('t', 'T', 'x'); m.AddOptionComboBox('k', 'K', 'a\\nb', 0)
    m.Storage.seed = 'from init'";

#[test]
fn module_exposes_the_selected_site() {
    let session = open_module(SETTINGS, "");
    assert_eq!(
        session.eval("MODULE.Name .. MODULE.TotalDirectory"),
        "Site2"
    );
    let storage = "MODULE.Storage.seed .. '|' .. MODULE.Storage.missing";
    assert_eq!(session.eval(storage), "from init|");
    assert_eq!(session.eval("MODULE.GetOption('n') + 1"), "6");

    session.select_directory(1).unwrap();
    assert_eq!(session.eval("MODULE.CurrentDirectoryIndex"), "1");
    let beyond = session.select_directory(2).unwrap_err();
    assert_eq!(beyond.kind(), ErrorKind::Unsupported);
}

#[test]
fn settings_take_only_values_that_suit_them() {
    let session = open_module(SETTINGS, "");
    session.set_option("n", OptionValue::Int(7)).unwrap();
    session.set_option("c", OptionValue::Bool(true)).unwrap();
    session
        .set_option("t", OptionValue::Text("42".into()))
        .unwrap();
    session.set_option("k", OptionValue::Int(1)).unwrap();
    assert_eq!(session.eval("MODULE.GetOption('n') + 1"), "8");
    assert_eq!(session.eval("tostring(MODULE.GetOption('c'))"), "true");
    assert_eq!(session.eval("type(MODULE.GetOption('t'))"), "string");

    for (name, value) in [
        ("missing", OptionValue::Int(1)),
        ("n", OptionValue::Text("7".into())),
        ("c", OptionValue::Int(1)),
        ("k", OptionValue::Int(2)),
    ] {
        let err = session.set_option(name, value).unwrap_err();
        assert_eq!(err.kind(), ErrorKind::Unsupported, "{name}: {err}");
    }
    assert_eq!(session.eval("MODULE.GetOption('k')"), "1");
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
    let init = "local m = NewWebsiteModule(); m.Name = 'L'; m.OnLogin = 'Login'";
    let session = open_module(init, LOGIN);
    assert_eq!(session.login().unwrap_err().kind(), ErrorKind::Unsupported);
    assert_eq!(session.eval("MODULE.Account.Status"), "-1");

    session.set_account("good", "pw");
    assert!(session.login().unwrap());
    assert_eq!(session.account_status(), AccountStatus::Valid);

    session.set_account("bad", "pw");
    assert_eq!(
        session.login().unwrap_err().kind(),
        ErrorKind::LoginRejected
    );
}

#[test]
fn directory_pages_reads_pagenumber() {
    let init = "local m = NewWebsiteModule(); m.Name = 'D'; m.OnGetDirectoryPageNumber = 'Pages'";
    let session = open_module(
        init,
        "function Pages() PAGENUMBER = 12.0; return no_error end",
    );
    assert_eq!(session.directory_pages().unwrap(), 12);
    let without_handler = open_module("NewWebsiteModule()", "");
    assert_eq!(without_handler.directory_pages().unwrap(), 1);
}

#[test]
fn after_image_saved_may_edit_only_that_page() {
    let pages = TempDir::new().unwrap();
    let page = pages.path().join("0001.jpg");
    std::fs::write(&page, "ORIGINAL").unwrap();
    let init = "local m = NewWebsiteModule(); m.Name = 'W'; m.OnAfterImageSaved = 'Edit'";
    let rest =
        "function Edit() local f = io.open(FILENAME, 'wb'); f:write('EDITED'); f:close() end";
    let session = open_module(init, rest);

    session.after_image_saved(&page).unwrap();
    assert_eq!(std::fs::read_to_string(&page).unwrap(), "EDITED");
    // Once the hook is over, the page is out of reach again.
    assert_eq!(session.eval("tostring(io.open(FILENAME, 'w'))"), "nil");
}

const LOOPING_INIT: &str = "local m = NewWebsiteModule(); m.Name = 'Loop'
    m.OnGetInfo = 'Spin'; m.OnCheckSite = 'Fine'; m.OnTaskStart = 'InCoroutine'
    m.OnGetPageNumber = 'CancelThenSpin'; m.OnLogin = 'Swallow'; m.OnGetImageURL = 'Stubborn'";

const LOOPING: &str = "
    function Spin() while true do end end
    function Fine() RAN = true; return 'fine' end
    function InCoroutine() coroutine.wrap(function() while true do end end)() end
    function CancelThenSpin() cancel(); while true do end end
    function Swallow() cancel(); pcall(function() while true do end end); return 'looks fine' end
    function Stubborn() cancel(); while true do pcall(function() for i = 1, 50 do end end) end end";

/// Opens a module whose handlers loop, with `cancel()` cancelling its own
/// scrape, letting each call run `cpu_limit` instructions.
fn looping_session(cpu_limit: u64) -> (Session, Checkout, CancelToken) {
    let checkout = Checkout::new(&[("modules/Loop.lua", &module_source(LOOPING_INIT, LOOPING))]);
    let cancel = CancelToken::new();
    let site = checkout.site("modules/Loop.lua", None, None).unwrap();
    let config = SessionConfig {
        cpu_limit,
        ..SessionConfig::default()
    };
    let session = site.open(&config, cancel.clone()).unwrap();
    let token = cancel.clone();
    let cancel_from_lua = session.lua().create_function(move |_, ()| {
        token.cancel();
        Ok(())
    });
    session
        .lua()
        .globals()
        .set("cancel", cancel_from_lua.unwrap())
        .unwrap();
    (session, checkout, cancel)
}

#[test]
fn a_runaway_loop_stops_at_the_cpu_limit() {
    let (session, _, _) = looping_session(1_000_000);
    let err = session.call(Event::GetInfo).unwrap_err();
    assert_eq!(err.kind(), ErrorKind::CpuLimit, "{err}");
    assert!(err.to_string().contains("ran too long"), "{err}");
    // The limit is per call, so the session is still usable.
    let fine = session.call(Event::CheckSite).unwrap();
    assert_eq!(session.lua().coerce_string(fine).unwrap().unwrap(), "fine");
    assert!(session.peak_instructions() >= 1_000_000);
}

#[test]
fn the_cpu_limit_reaches_into_coroutines() {
    let (session, _, _) = looping_session(1_000_000);
    let err = session.call(Event::TaskStart).unwrap_err();
    assert_eq!(err.kind(), ErrorKind::CpuLimit, "{err}");
}

#[test]
fn cancelling_stops_the_running_call_and_every_later_one() {
    let (session, checkout, cancel) = looping_session(SessionConfig::default().cpu_limit);
    let running = session.call(Event::GetPageNumber).unwrap_err();
    assert_eq!(running.kind(), ErrorKind::Cancelled, "{running}");
    let later = session.call(Event::CheckSite).unwrap_err();
    assert_eq!(later.kind(), ErrorKind::Cancelled, "{later}");
    // Read from Rust: a cancelled session runs no more Lua, not even this.
    assert!(
        session
            .lua()
            .globals()
            .get::<Value>("RAN")
            .unwrap()
            .is_nil()
    );

    let site = checkout.site("modules/Loop.lua", None, None).unwrap();
    let opening = site.open(&SessionConfig::default(), cancel).err().unwrap();
    assert_eq!(opening.kind(), ErrorKind::Cancelled, "{opening}");
}

#[test]
fn a_caught_termination_is_still_a_cancellation() {
    let (session, _, _) = looping_session(SessionConfig::default().cpu_limit);
    let err = session.call(Event::Login).unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Cancelled, "{err}");
}

#[test]
fn a_loop_that_keeps_catching_the_termination_still_stops() {
    let (session, _, _) = looping_session(SessionConfig::default().cpu_limit);
    let err = session.call(Event::GetImageUrl).unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Cancelled, "{err}");
}
