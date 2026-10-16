use std::path::Path;

use tempfile::TempDir;

use super::*;
use crate::testing::{Checkout, module_source, open_module};
use crate::{CancelToken, Page, SessionConfig};

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

/// How many times a decode of about 1 MB of JSON the CPU limit must allow.
/// Below it, a slow but legitimate handler could be stopped.
const JSON_HEADROOM: u64 = 10;

/// About 1 MB of JSON shaped like a chapter list API response.
fn chapter_list_json() -> String {
    let chapters: Vec<String> = (0..8000)
        .map(|i| {
            format!(
                r#"{{"id":{i},"title":"Chapter {i}: A reasonably long chapter title","slug":"chapter-{i}","group":{{"id":7,"name":"Scans"}}}}"#
            )
        })
        .collect();
    format!(r#"{{"data":[{}]}}"#, chapters.join(","))
}

#[test]
#[ignore = "needs an FMD2 checkout in ATSUME_FMD2_DIR"]
fn the_cpu_limit_leaves_room_for_a_large_json_decode() {
    let lua_dir = crate::testing::fmd2_lua_dir();
    let source =
        "function Init() local m = NewWebsiteModule(); m.Name = 'Json'; m.OnGetInfo = 'GetInfo' end
        function GetInfo() return #require('utils.json').decode(DATA).data end";
    let module = TempDir::new().unwrap();
    let file = module.path().join("Json.lua");
    std::fs::write(&file, source).unwrap();
    let site = crate::Catalog::load_file(&lua_dir, &file)
        .unwrap()
        .remove(0);
    let session = site
        .open(&SessionConfig::default(), CancelToken::new())
        .unwrap();
    let json = chapter_list_json();
    session.lua().globals().set("DATA", json.as_str()).unwrap();

    let start = std::time::Instant::now();
    let decoded = session.call(Event::GetInfo).unwrap();
    let elapsed = start.elapsed();
    assert_eq!(decoded.as_integer(), Some(8000));

    let used = session.peak_instructions();
    let limit = SessionConfig::default().cpu_limit;
    eprintln!(
        "decoding {} bytes used {used} instructions in {elapsed:?}; the limit is {}x that",
        json.len(),
        limit / used.max(1)
    );
    assert!(
        used * JSON_HEADROOM <= limit,
        "the limit is under {JSON_HEADROOM}x a 1 MB JSON decode"
    );
}

const INFO: &str = "
function Info()
    MANGAINFO.Title = 'Solo Leveling'
    MANGAINFO.Authors = 'Chugong'
    MANGAINFO.Status = MangaInfoStatusIfPos('Status: Completed')
    MANGAINFO.ChapterLinks.Add('/manga/solo/1'); MANGAINFO.ChapterNames.Add('Chapter 1')
    MANGAINFO.ChapterLinks.Add('manga/solo/2'); MANGAINFO.ChapterNames.Add('Chapter 2')
    SEEN = MANGAINFO.URL
    if URL:find('gone') then return information_not_found end
    return no_error
end";

#[test]
fn info_returns_what_the_module_recorded() {
    let session = open_module(
        "local m = NewWebsiteModule(); m.Name = 'S'; m.OnGetInfo = 'Info'",
        INFO,
    );
    let info = session.info("/manga/solo/").unwrap();
    assert_eq!(info.title, "Solo Leveling");
    assert_eq!(info.authors, "Chugong");
    assert_eq!(info.status, Some(crate::SeriesStatus::Completed));
    let chapters: Vec<_> = info
        .chapters
        .iter()
        .map(|c| (c.url.as_str(), c.display_name()))
        .collect();
    assert_eq!(
        chapters,
        [
            ("/manga/solo/1", "Chapter 1"),
            ("/manga/solo/2", "Chapter 2")
        ]
    );
    assert_eq!(session.eval("SEEN"), "/manga/solo/");

    // Each call starts from an empty record, so nothing carries over.
    let again = session.info("/manga/solo/").unwrap();
    assert_eq!(again.chapters.len(), 2);
}

#[test]
fn info_reports_a_page_without_a_series() {
    let session = open_module(
        "local m = NewWebsiteModule(); m.Name = 'S'; m.OnGetInfo = 'Info'",
        INFO,
    );
    let err = session.info("/manga/gone/").unwrap_err();
    assert_eq!(err.kind(), ErrorKind::NotFound);
    assert!(err.to_string().contains("/manga/gone/"), "{err}");
}

#[test]
fn status_if_pos_takes_keyword_lists_from_the_module() {
    let session = open_module("NewWebsiteModule()", "");
    assert_eq!(
        session.eval("MangaInfoStatusIfPos('En curso', 'en curso', 'finalizado')"),
        "ongoing"
    );
    assert_eq!(
        session.eval("MangaInfoStatusIfPos('Finalizado', 'en curso', 'finalizado')"),
        "completed"
    );
    assert_eq!(
        session.eval("MangaInfoStatusIfPos('Pausa', nil, nil, 'pausa|break')"),
        "hiatus"
    );
    assert_eq!(session.eval("MangaInfoStatusIfPos('nothing known')"), "");
}

const PAGES_INIT: &str =
    "local m = NewWebsiteModule(); m.Name = 'P'; m.RootURL = 'https://site.example'
    m.OnGetPageNumber = 'Pages'; m.OnGetImageURL = 'Image'";

#[test]
fn pages_lists_images_against_the_site() {
    let rest = "function Pages()
        TASK.PageLinks.Add('/p/1.jpg'); TASK.PageLinks.Add('//cdn.example/2.jpg')
        return true end
        function Image() return true end";
    let session = open_module(PAGES_INIT, rest);
    let pages = session.pages("/chapter/1").unwrap();
    assert_eq!(
        pages,
        [
            Page::Image("https://site.example/p/1.jpg".into()),
            Page::Image("https://cdn.example/2.jpg".into()),
        ]
    );
}

#[test]
fn pages_found_only_when_asked_are_resolved_one_at_a_time() {
    // Shaped like Taadd: the chapter lists each page's web page, and the
    // image is read from it.
    let rest = "function Pages()
        for i = 1, 3 do TASK.PageContainerLinks.Add('/c/1/' .. i) end
        TASK.PageNumber = TASK.PageContainerLinks.Count
        return no_error end
        function Image()
        CALLS = (CALLS or 0) + 1
        TASK.PageLinks[WORKID] = TASK.PageContainerLinks[WORKID] .. '.jpg?from=' .. URL
        return true end";
    let session = open_module(PAGES_INIT, rest);
    let pages = session.pages("/chapter/1").unwrap();
    assert_eq!(
        pages,
        [Page::Unresolved, Page::Unresolved, Page::Unresolved]
    );
    let second = session.resolve_page("/chapter/1", 1).unwrap();
    assert_eq!(second, "https://site.example/c/1/2.jpg?from=/chapter/1");
    // A resolved page is not asked for again.
    session.resolve_page("/chapter/1", 1).unwrap();
    assert_eq!(session.eval("CALLS"), "1");
}

#[test]
fn a_module_may_resolve_every_page_on_the_first_call() {
    // Shaped like MangaTown, which reads all images from the first page.
    let rest = "function Pages() TASK.PageNumber = 2; return true end
        function Image()
        CALLS = (CALLS or 0) + 1
        if WORKID == 0 then TASK.PageLinks.Add('/1.jpg'); TASK.PageLinks.Add('/2.jpg') end
        return true end";
    let session = open_module(PAGES_INIT, rest);
    assert_eq!(session.pages("/chapter/1").unwrap().len(), 2);
    session.resolve_page("/chapter/1", 0).unwrap();
    assert_eq!(
        session.resolve_page("/chapter/1", 1).unwrap(),
        "https://site.example/2.jpg"
    );
    assert_eq!(session.eval("CALLS"), "1");
}

#[test]
fn a_page_is_resolved_only_for_the_chapter_last_listed() {
    let rest = "function Pages() TASK.PageNumber = 1; return true end
        function Image() return true end";
    let session = open_module(PAGES_INIT, rest);
    let unlisted = session.resolve_page("/chapter/1", 0).unwrap_err();
    assert_eq!(unlisted.kind(), ErrorKind::Unsupported);
    session.pages("/chapter/1").unwrap();
    let empty = session.resolve_page("/chapter/1", 0).unwrap_err();
    assert_eq!(empty.kind(), ErrorKind::Module, "{empty}");
    assert!(empty.to_string().contains("no image address"), "{empty}");
}

#[test]
fn pages_reports_a_refusal_or_no_pages() {
    let refused = open_module(PAGES_INIT, "function Pages() return false end");
    let err = refused.pages("/chapter/1").unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Module);
    assert!(
        err.to_string()
            .contains("declined reading pages for /chapter/1"),
        "{err}"
    );

    let empty = open_module(PAGES_INIT, "function Pages() return true end");
    assert_eq!(
        empty.pages("/chapter/1").unwrap_err().kind(),
        ErrorKind::NotFound
    );
}

#[test]
fn counted_pages_need_a_way_to_resolve_them() {
    let init = "local m = NewWebsiteModule(); m.Name = 'P'; m.OnGetPageNumber = 'Pages'";
    let rest = "function Pages() TASK.PageNumber = 5; TASK.PageLinks.Add('https://a.example/1.jpg'); return true end";
    let session = open_module(init, rest);
    assert_eq!(session.pages("/chapter/1").unwrap().len(), 1);
}

const LISTING: &str = "
function List()
    UPDATELIST.UpdateStatusText('reading page ' .. (URL + 1))
    LINKS.Add('manga/' .. URL); NAMES.Add('Series ' .. URL)
    if URL == 0 then LINKS.Add('/manga/extra'); UPDATELIST.CurrentDirectoryPageNumber = 3 end
    return no_error
end";

#[test]
fn list_reads_one_directory_page() {
    let init = "local m = NewWebsiteModule(); m.Name = 'D'; m.OnGetNameAndLink = 'List'";
    let session = open_module(init, LISTING);
    let messages = std::rc::Rc::new(std::cell::RefCell::new(Vec::new()));
    let sink = std::rc::Rc::clone(&messages);
    session.on_status(move |text| sink.borrow_mut().push(text.to_owned()));

    let first = session.list(0).unwrap();
    let entries: Vec<_> = first
        .entries
        .iter()
        .map(|e| (e.url.as_str(), e.display_name()))
        .collect();
    assert_eq!(
        entries,
        [("/manga/0", "Series 0"), ("/manga/extra", "/manga/extra")]
    );
    assert_eq!(first.reported_pages, Some(3));

    // Each page starts from empty lists and no reported count.
    let second = session.list(1).unwrap();
    assert_eq!(second.entries.len(), 1);
    assert_eq!(second.reported_pages, None);
    assert_eq!(*messages.borrow(), ["reading page 1", "reading page 2"]);
}
