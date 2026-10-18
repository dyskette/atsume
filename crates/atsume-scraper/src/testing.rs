//! Helpers the crate's tests share.

use std::path::PathBuf;
use std::sync::{Arc, Mutex};

use mlua::{Lua, Value};
use tempfile::TempDir;

use crate::catalog::select_site;
use crate::error::ErrorKind;
use crate::http::{HttpConfig, Request, Response, Transport, TransportError};
use crate::runtime::convert::lua_text;
use crate::{CancelToken, Catalog, Error, Result, Session, SessionConfig, Site};

/// A checkout laid out like FMD2's, in a temporary directory.
pub(crate) struct Checkout {
    dir: TempDir,
}

impl Checkout {
    /// Creates the checkout and writes each `(path, source)` pair under its
    /// `lua` directory.
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

    /// Returns the path of `file`, given relative to the `lua` directory.
    pub(crate) fn path_of(&self, file: &str) -> PathBuf {
        self.lua_dir().join(file)
    }

    /// Returns the websites the module file `file` declares.
    pub(crate) fn sites(&self, file: &str) -> Result<Vec<Site>> {
        Catalog::load_file(&self.lua_dir(), &self.path_of(file))
    }

    /// Returns the website named `site_name` that `file` declares, or the
    /// first it declares.
    pub(crate) fn site(
        &self,
        file: &str,
        site_name: Option<&str>,
        root_url: Option<&str>,
    ) -> Result<Site> {
        let sites = self.sites(file)?;
        let Some(site_name) = site_name else {
            return Ok(sites[0].clone());
        };
        select_site(&sites, site_name, root_url).ok_or_else(|| {
            let message = format!("no website named {site_name:?}");
            Error::new(ErrorKind::Unsupported, message)
        })
    }

    /// Opens a session for a website that `file` declares.
    pub(crate) fn open(
        &self,
        file: &str,
        site_name: Option<&str>,
        root_url: Option<&str>,
    ) -> Result<Session> {
        let site = self.site(file, site_name, root_url)?;
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

/// Opens a module that reaches the network through `http` and is cancelled by
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

/// Runs `source` in a plain Lua runtime after `register` has set its globals,
/// and returns what `source` returns as text. A boolean reads as `true` or
/// `false`, and any other value that has no text reads as its type name.
pub(crate) fn eval_registered(
    register: impl FnOnce(&Lua) -> mlua::Result<()>,
    source: &str,
) -> mlua::Result<String> {
    let lua = Lua::new();
    register(&lua)?;
    let value: Value = lua.load(source).eval()?;
    if let Value::Boolean(flag) = value {
        return Ok(flag.to_string());
    }
    Ok(lua_text(&lua, &value).unwrap_or_else(|| value.type_name().to_owned()))
}

/// Returns the `lua` directory of the FMD2 checkout that `ATSUME_FMD2_DIR`
/// names. Tests marked `#[ignore]` use it when they need upstream's modules.
pub(crate) fn fmd2_lua_dir() -> PathBuf {
    let checkout =
        std::env::var_os("ATSUME_FMD2_DIR").expect("set ATSUME_FMD2_DIR to an FMD2 checkout");
    PathBuf::from(checkout).join("lua")
}

/// A transport that answers each request from a function and records every
/// request it was sent.
///
/// The function receives the request and how many requests have arrived so
/// far, counting this one, so a test can answer the first request differently
/// from a retry.
pub(crate) struct Scripted<F> {
    answer: F,
    /// Every request sent so far, in order.
    pub(crate) seen: Mutex<Vec<Request>>,
}

impl<F> Transport for Scripted<F>
where
    F: Fn(&Request, usize) -> Result<Response, TransportError> + Send + Sync,
{
    fn send(&self, request: &Request) -> Result<Response, TransportError> {
        let mut seen = self.seen.lock().unwrap();
        seen.push(request.clone());
        let count = seen.len();
        drop(seen);
        (self.answer)(request, count)
    }
}

/// Returns a [`Scripted`] transport that answers with `answer`.
pub(crate) fn scripted<F>(answer: F) -> Arc<Scripted<F>>
where
    F: Fn(&Request, usize) -> Result<Response, TransportError> + Send + Sync,
{
    Arc::new(Scripted {
        answer,
        seen: Mutex::new(Vec::new()),
    })
}
