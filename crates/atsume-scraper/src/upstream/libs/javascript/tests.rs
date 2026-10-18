use std::path::Path;
use std::time::{Duration, Instant};

use tempfile::TempDir;

use super::script_runner::{ScriptError, ScriptRunner};
use crate::testing::{fmd2_lua_dir, open_module};

/// Returns a runner whose `lua` directory is `lua_dir`.
fn runner_in(lua_dir: &Path) -> ScriptRunner {
    ScriptRunner::new(lua_dir.canonicalize().unwrap())
}

/// Runs `source` with an empty `lua` directory.
fn run(source: &str) -> Result<String, ScriptError> {
    let lua_dir = TempDir::new().unwrap();
    runner_in(lua_dir.path()).run(source)
}

/// Checks that each `(source, expected)` returns `expected`.
fn check(cases: &[(&str, &str)]) {
    for (source, expected) in cases {
        match run(source) {
            Ok(got) => assert_eq!(got, *expected, "{source}"),
            Err(error) => panic!("{source}: {error}"),
        }
    }
}

#[test]
fn a_script_returns_its_completion_value_as_text() {
    check(&[
        ("'hello';", "hello"),
        ("var a = 6; var b = 7; a * b;", "42"),
        // Modules end their scripts with JSON.stringify and parse the text in
        // Lua.
        (
            "var m = 1, ch = 2; JSON.stringify({m: m, ch: ch});",
            r#"{"m":1,"ch":2}"#,
        ),
        ("var x; x;", ""),
        ("null;", "null"),
        (r#"["a", "b"];"#, "a,b"),
        ("[1, 2, 3].map(x => x * 2).join(',')", "2,4,6"),
        (
            "var window = {}; window.nonce = 'abc'; window.nonce;",
            "abc",
        ),
        ("print('hi'); 'ok';", "ok"),
        (
            "console.warn('careful'); console.log('x', 1, {}); 'survived';",
            "survived",
        ),
    ]);
}

#[test]
fn a_script_runs_in_sloppy_mode_without_node_or_browser_globals() {
    check(&[
        // Strict mode would refuse an undeclared variable.
        ("undeclared = 5; undeclared;", "5"),
        ("typeof global", "undefined"),
        ("typeof window", "undefined"),
    ]);
}

#[test]
fn each_script_starts_from_a_clean_runtime() {
    let lua_dir = TempDir::new().unwrap();
    let runner = runner_in(lua_dir.path());
    runner.run("var leftover = 1;").unwrap();
    assert_eq!(runner.run("typeof leftover").unwrap(), "undefined");
}

#[test]
fn an_error_in_a_script_is_reported() {
    let syntax = run("this is not javascript").unwrap_err();
    assert!(
        matches!(&syntax, ScriptError::Thrown(text) if text.contains("SyntaxError")),
        "{syntax}"
    );
    let thrown_error = run("throw new Error('broken')").unwrap_err();
    assert_eq!(thrown_error.to_string(), "Error: broken");
    let thrown_string = run("throw 'plain'").unwrap_err();
    assert_eq!(thrown_string.to_string(), "plain");
}

#[test]
fn a_script_that_never_ends_is_stopped() {
    let lua_dir = TempDir::new().unwrap();
    let mut runner = runner_in(lua_dir.path());
    runner.time_limit = Duration::from_millis(200);

    let start = Instant::now();
    let endless = runner.run("while (true) {}").unwrap_err();
    assert!(
        start.elapsed() < Duration::from_secs(5),
        "took {:?}",
        start.elapsed()
    );
    assert!(matches!(endless, ScriptError::TimedOut(_)), "{endless}");
    // A try block cannot keep the script alive.
    let caught = runner
        .run("try { while (true) {} } catch (e) {} 'escaped'")
        .unwrap_err();
    assert!(matches!(caught, ScriptError::TimedOut(_)), "{caught}");
}

/// Writes `files`, each a `(path, source)` pair, under a new `lua` directory.
fn lua_dir_with(files: &[(&str, &str)]) -> TempDir {
    let checkout = TempDir::new().unwrap();
    for (name, source) in files {
        let path = checkout.path().join("lua").join(name);
        std::fs::create_dir_all(path.parent().unwrap()).unwrap();
        std::fs::write(path, source).unwrap();
    }
    checkout
}

#[test]
fn require_runs_a_helper_once_as_a_commonjs_module() {
    let checkout = lua_dir_with(&[(
        "utils/greet.js",
        "LOADS = (typeof LOADS === 'number' ? LOADS : 0) + 1;
         (function (root, factory) {
             if (typeof exports === 'object') { module.exports = factory(); }
             else { root.greet = factory(); }
         }(this, function () { return function (n) { return 'hi ' + n; }; }));",
    )]);
    let runner = runner_in(&checkout.path().join("lua"));
    let source = "var a = require('utils/greet.js'), b = require('utils.greet');
        var c = require('utils\\\\greet.js');
        [a('x'), a === b && b === c, LOADS].join(' ')";
    assert_eq!(runner.run(source).unwrap(), "hi x true 1");
}

#[test]
fn require_reads_nothing_outside_the_lua_directory() {
    let checkout = lua_dir_with(&[("utils/ok.js", "module.exports = 1;")]);
    std::fs::write(
        checkout.path().join("secret.js"),
        "module.exports = 'leaked';",
    )
    .unwrap();
    let runner = runner_in(&checkout.path().join("lua"));
    for name in [
        "../secret.js",
        "utils/../../secret.js",
        "/etc/passwd",
        "utils/../../../../../../etc/passwd",
        "utils/missing.js",
    ] {
        let error = runner.run(&format!("require({name:?})")).unwrap_err();
        assert!(error.to_string().contains("require("), "{name}: {error}");
    }
}

#[test]
fn require_crypto_serves_random_numbers() {
    check(&[
        (
            "var a = require('crypto').getRandomValues(new Uint32Array(4));
             [a.length, a.some(v => v !== 0)].join(' ')",
            "4 true",
        ),
        (
            "typeof require('crypto').randomBytes(4).readInt32LE()",
            "number",
        ),
        ("require('crypto').randomBytes(3).length", "3"),
        ("require('crypto').randomBytes(2).readInt32LE()", "0"),
    ]);
    for source in [
        "require('crypto').getRandomValues(5)",
        "require('crypto').getRandomValues({length: 1e9})",
        "require('crypto').randomBytes(-1)",
        "require('crypto').randomBytes(1 << 21)",
    ] {
        assert!(run(source).is_err(), "{source}");
    }
}

#[test]
fn get_random_values_fills_at_most_65536_bytes_whatever_the_element_size() {
    check(&[
        (
            "require('crypto').getRandomValues(new Uint32Array(16384)).length",
            "16384",
        ),
        (
            "require('crypto').getRandomValues(new Uint8Array(65536)).length",
            "65536",
        ),
        // An object without a byteLength counts four bytes per element.
        (
            "require('crypto').getRandomValues({length: 16384}).length",
            "16384",
        ),
    ]);
    for source in [
        "require('crypto').getRandomValues(new Uint32Array(16385))",
        "require('crypto').getRandomValues(new Uint8Array(65537))",
        "require('crypto').getRandomValues({length: 16385})",
        // A small byteLength does not let a long array through.
        "require('crypto').getRandomValues({length: 1e9, byteLength: 1})",
    ] {
        let error = run(source).unwrap_err();
        assert!(
            error.to_string().contains("at most 65536 bytes"),
            "{source}: {error}"
        );
    }
}

#[test]
fn modules_run_scripts_through_exec_js() {
    let session = open_module("NewWebsiteModule()", "");
    let exec = |source: &str| session.eval(&format!("require('fmd.duktape').ExecJS({source:?})"));
    assert_eq!(exec("1 + 1"), "2");
    assert_eq!(exec("[1, 2, 3].map(x => x * 2).join(',')"), "2,4,6");
    assert_eq!(exec("'a' + 'b'"), "ab");

    let failure =
        "tostring(select(2, pcall(require('fmd.duktape').ExecJS, \"throw new Error('broken')\")))";
    assert!(
        session
            .eval(failure)
            .contains("fmd.duktape.ExecJS: Error: broken")
    );
}

#[test]
#[ignore = "needs an FMD2 checkout in ATSUME_FMD2_DIR"]
fn upstream_crypto_js_helpers_round_trip_a_page_list() {
    let runner = ScriptRunner::new(fmd2_lua_dir().canonicalize().unwrap());
    let source = r#"
        var CryptoJS = require("utils/crypto-js.min.js");
        var CryptoJSAesJson = require("utils/cryptojs-aes-format.js");
        var secret = CryptoJS.AES.encrypt(
            JSON.stringify(["p1.jpg", "p2.jpg"]), "passphrase",
            { format: CryptoJSAesJson }
        ).toString();
        CryptoJS.AES.decrypt(secret, "passphrase", { format: CryptoJSAesJson })
            .toString(CryptoJS.enc.Utf8);
    "#;
    assert_eq!(runner.run(source).unwrap(), r#"["p1.jpg","p2.jpg"]"#);
}
