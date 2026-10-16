use std::path::Path;

use tempfile::TempDir;

use super::*;
use crate::convert::text_of;

/// Runs `source` in a fresh sandbox over the checkout at `root` and returns
/// its result as text.
fn eval_in(root: &Path, writable: Option<&Path>, source: &str) -> mlua::Result<String> {
    let lua = new_runtime(&root.join("lua"))?;
    lua.set_app_data(WritableFile(writable.map(Path::to_path_buf)));
    let value: Value = lua.load(source).eval()?;
    Ok(text_of(&lua, &value).unwrap_or_else(|| value.type_name().to_owned()))
}

fn eval(root: &Path, source: &str) -> String {
    eval_in(root, None, source).unwrap_or_else(|e| panic!("{source}: {e}"))
}

/// Quotes `s` as a Lua long string, so paths need no escaping.
fn quote(s: &Path) -> String {
    format!("[==[{}]==]", s.display())
}

fn write(path: &Path, contents: &str) {
    std::fs::create_dir_all(path.parent().unwrap()).unwrap();
    std::fs::write(path, contents).unwrap();
}

/// Links `link` to the directory `target`, or returns false where the
/// platform refuses, as Windows does without developer mode.
fn symlink_dir(target: &Path, link: &Path) -> bool {
    #[cfg(unix)]
    let linked = std::os::unix::fs::symlink(target, link);
    #[cfg(windows)]
    let linked = std::os::windows::fs::symlink_dir(target, link);
    linked.is_ok()
}

#[test]
fn modules_get_the_libraries_they_use() {
    let root = TempDir::new().unwrap();
    for name in [
        "string.format",
        "table.unpack",
        "math.floor",
        "utf8.char",
        "os.time",
        "os.date",
        "os.clock",
        "os.remove",
        "os.rename",
        "io.open",
        "io.lines",
        "require",
        "package.loaded",
        "package.preload",
        "pcall",
        "coroutine.wrap",
        "load",
    ] {
        assert_ne!(
            eval(root.path(), &format!("return type({name})")),
            "nil",
            "{name} is missing"
        );
    }
}

#[test]
fn nothing_reaches_outside_the_runtime() {
    let root = TempDir::new().unwrap();
    for name in [
        "debug",
        "dofile",
        "loadfile",
        "os.execute",
        "os.exit",
        "os.getenv",
        "os.tmpname",
        "os.setlocale",
        "io.popen",
        "io.write",
        "io.output",
        "io.tmpfile",
        "package.path",
        "package.cpath",
        "package.loadlib",
        "package.searchers",
        "package.searchpath",
    ] {
        assert_eq!(
            eval(root.path(), &format!("return type({name})")),
            "nil",
            "{name} is available"
        );
    }
}

#[test]
fn load_refuses_bytecode() {
    let root = TempDir::new().unwrap();
    let source =
        "local f, err = load(string.dump(function() end)); return tostring(f) .. ' ' .. err";
    let got = eval(root.path(), source);
    assert!(got.starts_with("nil ") && got.contains("binary"), "{got}");
    assert_eq!(eval(root.path(), "return load('return 1 + 1')()"), "2");
    assert_eq!(
        eval(
            root.path(),
            "return load('return x', 'chunk', 'b', {x = 'env'})()"
        ),
        "env"
    );
}

#[test]
fn allocation_is_capped() {
    let root = TempDir::new().unwrap();
    let source = "return string.rep('x', 1024 * 1024 * 1024)";
    let err = eval_in(root.path(), None, source).unwrap_err();
    assert!(matches!(err, mlua::Error::MemoryError(_)), "{err}");
}

#[test]
fn reads_inside_the_checkout() {
    let root = TempDir::new().unwrap();
    let map = root.path().join("userdata").join("map.txt");
    write(&map, "1;a\r\n2;b\n3;c");
    let cases = [
        (
            "local f = io.open('userdata/map.txt', 'rb'); local s = f:read('*all'); f:close(); return #s",
            "12",
        ),
        (
            "local f = io.open('userdata/map.txt'); local a, b = f:read('l', 'L'); return a .. '|' .. b",
            "1;a|2;b\n",
        ),
        (
            "local t = {}; for l in io.lines('userdata/map.txt') do t[#t+1] = l end; return table.concat(t, ',')",
            "1;a,2;b,3;c",
        ),
        (
            "local n = 0; for _ in io.open('userdata/map.txt'):lines() do n = n + 1 end; return n",
            "3",
        ),
        (
            "local f, err = io.open('userdata/none.txt'); return tostring(f) .. ' ' .. err",
            "nil userdata/none.txt: No such file or directory",
        ),
    ];
    for (source, want) in cases {
        assert_eq!(eval(root.path(), source), want, "{source}");
    }
    let absolute = format!("return io.open({}):read('a'):sub(1, 3)", quote(&map));
    assert_eq!(eval(root.path(), &absolute), "1;a");

    let closed = "local f = io.open('userdata/map.txt'); f:close(); return f:read('a')";
    assert!(eval_in(root.path(), None, closed).is_err());
}

#[test]
fn reads_cannot_leave_the_checkout() {
    let outside = TempDir::new().unwrap();
    let secret = outside.path().join("secret.txt");
    write(&secret, "secret");
    let root = TempDir::new().unwrap();
    let mut paths = vec![
        secret.display().to_string(),
        format!(
            "../{}/secret.txt",
            outside.path().file_name().unwrap().to_string_lossy()
        ),
        "/etc/passwd".to_owned(),
    ];
    if symlink_dir(outside.path(), &root.path().join("escape")) {
        paths.push("escape/secret.txt".to_owned());
    }
    for path in paths {
        let path = Path::new(&path);
        let opened = eval(
            root.path(),
            &format!("return tostring(io.open({}))", quote(path)),
        );
        assert_eq!(opened, "nil", "io.open({})", path.display());
        let lines = format!("for _ in io.lines({}) do end", quote(path));
        assert!(eval_in(root.path(), None, &lines).is_err(), "{lines}");
    }
}

#[test]
fn writes_fail_as_on_a_read_only_filesystem() {
    let root = TempDir::new().unwrap();
    let keep = root.path().join("keep.txt");
    write(&keep, "keep");
    for source in [
        "local f, err = io.open('keep.txt', 'w'); return tostring(f) .. ' ' .. err",
        "local f, err = io.open('new.txt', 'a'); return tostring(f) .. ' ' .. err",
        "local f, err = io.open('keep.txt', 'r+'); return tostring(f) .. ' ' .. err",
        "local ok, err = os.remove('keep.txt'); return tostring(ok) .. ' ' .. err",
        "local ok, err = os.rename('keep.txt', 'moved.txt'); return tostring(ok) .. ' ' .. err",
    ] {
        assert!(eval(root.path(), source).starts_with("nil "), "{source}");
    }
    assert_eq!(std::fs::read_to_string(&keep).unwrap(), "keep");
    assert!(!root.path().join("new.txt").exists());
    assert!(!root.path().join("moved.txt").exists());
}

#[test]
fn only_the_writable_file_may_be_written() {
    let root = TempDir::new().unwrap();
    let pages = TempDir::new().unwrap();
    let (page, other) = (
        pages.path().join("0001.jpg"),
        pages.path().join("other.jpg"),
    );
    write(&page, "ORIGINAL");
    write(&other, "ORIGINAL");
    let run = |source: String| eval_in(root.path(), Some(&page), &source).unwrap();

    assert_eq!(
        run(format!("return io.open({}, 'rb'):read('a')", quote(&page))),
        "ORIGINAL"
    );
    run(format!(
        "local f = io.open({}, 'wb'); f:write('ED', 'IT'):write(1); f:close()",
        quote(&page)
    ));
    run(format!(
        "local f = io.open({}, 'a'); f:write('+'); f:close()",
        quote(&page)
    ));
    // Nothing is saved without close, so an abandoned edit leaves the page.
    run(format!(
        "local f = io.open({}, 'w'); f:write('LOST')",
        quote(&page)
    ));
    assert_eq!(std::fs::read_to_string(&page).unwrap(), "EDIT1+");

    for source in [
        format!(
            "local f, err = io.open({}, 'w'); return tostring(f) .. ' ' .. err",
            quote(&other)
        ),
        format!(
            "local f, err = io.open({}, 'r+'); return tostring(f) .. ' ' .. err",
            quote(&page)
        ),
        format!(
            "local ok, err = io.open({}):write('x'); return tostring(ok) .. ' ' .. err",
            quote(&page)
        ),
    ] {
        assert!(run(source.clone()).starts_with("nil "), "{source}");
    }

    // Once the hook is over, the page is out of reach again.
    let source = format!("return tostring(io.open({}, 'w'))", quote(&page));
    let reopened = eval_in(root.path(), None, &source).unwrap();
    assert_eq!(reopened, "nil");
    assert_eq!(std::fs::read_to_string(&other).unwrap(), "ORIGINAL");
}

#[test]
fn require_loads_from_the_lua_directory() {
    let root = TempDir::new().unwrap();
    let lua = root.path().join("lua");
    write(
        &lua.join("utils/counter.lua"),
        "LOADS = (LOADS or 0) + 1; return {n = LOADS}",
    );
    write(&lua.join("templates/X/init.lua"), "return 'init'");
    write(&lua.join("utils/nothing.lua"), "local x = 1");
    write(&lua.join("utils/bom.lua"), "\u{FEFF}return 'bom'");
    let cases = [
        (
            "local a = require 'utils.counter'; local b = require('utils.counter'); return a.n .. b.n .. tostring(a == b)",
            "11true",
        ),
        ("return require 'templates.X'", "init"),
        ("return tostring(require 'utils.nothing')", "true"),
        ("return require 'utils.bom'", "bom"),
        ("return tostring(require('utf8') == utf8)", "true"),
        (
            "local _, p = require 'templates.X'; return p:sub(-10):gsub('\\\\', '/')",
            "X/init.lua",
        ),
    ];
    for (source, want) in cases {
        assert_eq!(eval(root.path(), source), want, "{source}");
    }
}

#[test]
fn require_cannot_leave_the_lua_directory() {
    let outside = TempDir::new().unwrap();
    write(&outside.path().join("secret.lua"), "return 'secret'");
    let root = TempDir::new().unwrap();
    std::fs::create_dir_all(root.path().join("lua")).unwrap();
    write(&root.path().join("outside.lua"), "return 'secret'");
    let mut names = vec!["nope", "..outside", "..escape.secret"];
    if symlink_dir(outside.path(), &root.path().join("lua").join("escape")) {
        names.push("escape.secret");
    }
    for name in names {
        let err = eval_in(root.path(), None, &format!("return require '{name}'"))
            .unwrap_err()
            .to_string();
        assert!(
            err.contains(&format!("module '{name}' not found")),
            "{name}: {err}"
        );
    }
}

#[test]
fn lua_5_3_operators_behave_as_in_lua() {
    let root = TempDir::new().unwrap();
    for (expression, want) in [
        ("0xF0 & 0x3C", "48"),
        ("0xFF ~ 0x0F", "240"),
        ("~0", "-1"),
        ("-1 >> 60", "15"),
        ("1 << 64", "0"),
        ("256 >> -4", "4096"),
        ("-7 // 2", "-4"),
        ("7.5 // 2", "3.0"),
        ("1 | 2 & 3", "3"),
        ("2 + 3 << 1", "10"),
        ("(1 << 53) + 1 | 1", "9007199254740993"),
        ("1 << 63", "-9223372036854775808"),
    ] {
        assert_eq!(
            eval(root.path(), &format!("return {expression}")),
            want,
            "{expression}"
        );
    }
}
