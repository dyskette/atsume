//! Opens every upstream module, which runs each one's `Init()`.
//!
//! This exercises the whole surface a module touches at declaration time. It
//! needs an FMD2 checkout in `ATSUME_FMD2_DIR` and passes trivially without
//! one, since the modules cannot be committed here.

use std::collections::BTreeMap;
use std::path::PathBuf;

use atsume_scraper::{CancelToken, Host};

/// The share of upstream modules that must load.
const MIN_LOAD_RATE: f64 = 0.97;

#[test]
fn upstream_modules_load() {
    let Some(lua_dir) = std::env::var_os("ATSUME_FMD2_DIR").map(|d| PathBuf::from(d).join("lua"))
    else {
        eprintln!("skipped: set ATSUME_FMD2_DIR to an FMD2 checkout");
        return;
    };
    let files = module_files(&lua_dir).unwrap();
    assert!(
        files.len() > 500,
        "only {} modules found; the checkout looks wrong",
        files.len()
    );

    let host = Host::new(lua_dir);
    let mut failures = BTreeMap::new();
    for file in &files {
        if let Err(err) = host.open(file, None, None, CancelToken::new()) {
            let name = file.file_stem().unwrap().to_string_lossy().into_owned();
            failures.insert(name, first_line(&err.to_string()));
        }
    }

    for (name, err) in &failures {
        eprintln!("  {name}: {err}");
    }
    let loaded = files.len() - failures.len();
    #[expect(
        clippy::cast_precision_loss,
        reason = "module counts are far below 2^52"
    )]
    let rate = loaded as f64 / files.len() as f64;
    eprintln!(
        "{loaded}/{} modules loaded ({:.1}%)",
        files.len(),
        rate * 100.0
    );
    assert!(
        rate >= MIN_LOAD_RATE,
        "load rate below {:.0}%",
        MIN_LOAD_RATE * 100.0
    );
}

fn module_files(lua_dir: &std::path::Path) -> std::io::Result<Vec<PathBuf>> {
    let mut files = Vec::new();
    for entry in std::fs::read_dir(lua_dir.join("modules"))? {
        let path = entry?.path();
        if path.extension().is_some_and(|ext| ext == "lua") {
            files.push(path);
        }
    }
    files.sort();
    Ok(files)
}

/// Trims a Lua traceback down to its message.
fn first_line(message: &str) -> String {
    message.lines().next().unwrap_or_default().to_owned()
}
