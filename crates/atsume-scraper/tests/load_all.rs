//! Loads the catalog of every upstream module, which runs each one's `Init()`.
//!
//! This exercises the whole surface a module touches at declaration time. The
//! modules cannot be committed here, so the test is ignored by default: run it
//! with `make rust-corpus`, which needs an FMD2 checkout in `ATSUME_FMD2_DIR`.

use std::path::PathBuf;

use atsume_scraper::Catalog;

/// The share of upstream modules that must load.
const MIN_LOAD_RATE: f64 = 0.97;

#[test]
#[ignore = "needs an FMD2 checkout in ATSUME_FMD2_DIR"]
fn upstream_modules_load() {
    let checkout =
        std::env::var_os("ATSUME_FMD2_DIR").expect("set ATSUME_FMD2_DIR to an FMD2 checkout");
    let lua_dir = PathBuf::from(checkout).join("lua");
    let catalog = Catalog::load(lua_dir).unwrap();
    let failed = catalog.failures().len();
    let loaded_files = catalog
        .sites()
        .iter()
        .map(|site| &site.declaration().file)
        .collect::<std::collections::BTreeSet<_>>()
        .len();
    let files = loaded_files + failed;
    assert!(
        files > 500,
        "only {files} modules found; the checkout looks wrong"
    );

    for failure in catalog.failures() {
        let message = failure.error.to_string();
        let first_line = message.lines().next().unwrap_or_default();
        eprintln!("  {}: {first_line}", failure.file.display());
    }
    #[expect(
        clippy::cast_precision_loss,
        reason = "module counts are far below 2^52"
    )]
    let rate = loaded_files as f64 / files as f64;
    eprintln!(
        "{loaded_files}/{files} modules loaded ({:.1}%)",
        rate * 100.0
    );
    assert!(
        rate >= MIN_LOAD_RATE,
        "load rate below {:.0}%",
        MIN_LOAD_RATE * 100.0
    );
}
