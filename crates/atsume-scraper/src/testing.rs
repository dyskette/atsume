//! Helpers the crate's tests share.

use std::path::PathBuf;

use tempfile::TempDir;

use crate::catalog::select;
use crate::error::ErrorKind;
use crate::http::HttpConfig;
use crate::{CancelToken, Catalog, Error, Result, Session, SessionConfig, Site};

/// An FMD2-shaped checkout in a temporary directory.
pub(crate) struct Checkout {
    dir: TempDir,
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

    /// Returns the websites the module file `name` declares.
    pub(crate) fn sites(&self, name: &str) -> Result<Vec<Site>> {
        Catalog::load_file(&self.lua_dir(), &self.file(name))
    }

    /// Returns the website `site` declares in file `name`, or its first.
    pub(crate) fn site(
        &self,
        name: &str,
        site: Option<&str>,
        root_url: Option<&str>,
    ) -> Result<Site> {
        let sites = self.sites(name)?;
        let Some(site) = site else {
            return Ok(sites[0].clone());
        };
        select(&sites, site, root_url)
            .ok_or_else(|| Error::new(ErrorKind::Unsupported, format!("no website named {site:?}")))
    }

    /// Opens a session for a website declared in file `name`.
    pub(crate) fn open(
        &self,
        name: &str,
        site: Option<&str>,
        root_url: Option<&str>,
    ) -> Result<Session> {
        let site = self.site(name, site, root_url)?;
        site.open(&SessionConfig::default(), CancelToken::new())
    }
}

/// Returns a module file whose `Init()` is `init` and whose other code is
/// `rest`.
pub(crate) fn module_source(init: &str, rest: &str) -> String {
    format!("function Init()\n{init}\nend\n{rest}")
}

/// Opens a module whose `Init()` is `init` and whose other code is `rest`.
pub(crate) fn open_module(init: &str, rest: &str) -> Session {
    open_module_with(&HttpConfig::default(), CancelToken::new(), init, rest)
}

/// Opens a module that reaches the network through `http`, cancelled by
/// `cancel`.
pub(crate) fn open_module_with(
    http: &HttpConfig,
    cancel: CancelToken,
    init: &str,
    rest: &str,
) -> Session {
    let checkout = Checkout::new(&[("modules/Test.lua", &module_source(init, rest))]);
    let site = checkout.site("modules/Test.lua", None, None).unwrap();
    let config = SessionConfig {
        http: http.clone(),
        ..SessionConfig::default()
    };
    site.open(&config, cancel).unwrap()
}

/// Returns the `lua` directory of the FMD2 checkout in `ATSUME_FMD2_DIR`, for
/// tests marked `#[ignore]` that need upstream's modules.
pub(crate) fn fmd2_lua_dir() -> PathBuf {
    let checkout =
        std::env::var_os("ATSUME_FMD2_DIR").expect("set ATSUME_FMD2_DIR to an FMD2 checkout");
    PathBuf::from(checkout).join("lua")
}
