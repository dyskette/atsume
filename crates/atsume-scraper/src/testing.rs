//! Helpers the crate's tests share.

use std::path::PathBuf;

use tempfile::TempDir;

use crate::http::HttpConfig;
use crate::{CancelToken, Host, Result, Runner};

/// An FMD2-shaped checkout in a temporary directory.
pub(crate) struct Checkout {
    pub(crate) dir: TempDir,
}

impl Checkout {
    /// Writes each `(path, source)` under the checkout's `lua` directory.
    pub(crate) fn new(files: &[(&str, &str)]) -> Self {
        let dir = TempDir::new().unwrap();
        for (name, source) in files {
            let path = dir.path().join("lua").join(name);
            std::fs::create_dir_all(path.parent().unwrap()).unwrap();
            std::fs::write(path, source).unwrap();
        }
        Self { dir }
    }

    pub(crate) fn lua_dir(&self) -> PathBuf {
        self.dir.path().join("lua")
    }

    pub(crate) fn file(&self, name: &str) -> PathBuf {
        self.lua_dir().join(name)
    }

    pub(crate) fn open(
        &self,
        name: &str,
        site: Option<&str>,
        root_url: Option<&str>,
    ) -> Result<Runner> {
        Host::new(self.lua_dir()).open(&self.file(name), site, root_url, CancelToken::new())
    }
}

/// Returns a module file whose `Init()` is `init` and whose other code is
/// `rest`.
pub(crate) fn module_source(init: &str, rest: &str) -> String {
    format!("function Init()\n{init}\nend\n{rest}")
}

/// Opens a module whose `Init()` is `init` and whose other code is `rest`.
pub(crate) fn open_module(init: &str, rest: &str) -> Runner {
    let checkout = Checkout::new(&[("modules/Test.lua", &module_source(init, rest))]);
    checkout.open("modules/Test.lua", None, None).unwrap()
}

/// Opens a module that reaches the network through `http`, cancelled by
/// `cancel`.
pub(crate) fn open_module_with(
    http: HttpConfig,
    cancel: CancelToken,
    init: &str,
    rest: &str,
) -> Runner {
    let checkout = Checkout::new(&[("modules/Test.lua", &module_source(init, rest))]);
    let host = Host {
        lua_dir: checkout.lua_dir(),
        http,
    };
    host.open(&checkout.file("modules/Test.lua"), None, None, cancel)
        .unwrap()
}
