//! The websites the modules in a checkout declare.

use std::path::{Path, PathBuf};

use crate::cancel::CancelToken;
use crate::error::{Error, ErrorKind, Result};
use crate::module::Module;
use crate::runtime::Runtime;
use crate::session::{Session, SessionConfig};

/// Every website the modules in an FMD2 checkout declare.
///
/// Loading runs each module's `Init()` once, without network access. The
/// checkout is cloned at run time and never built in: the modules are
/// GPL-2.0-only, which is incompatible with atsume. See `docs/MODULES.md`.
#[derive(Clone, Debug)]
pub struct Catalog {
    sites: Vec<Site>,
    failures: Vec<LoadFailure>,
}

/// A module file that did not load, and why.
#[derive(Clone, Debug)]
pub struct LoadFailure {
    /// The module file.
    pub file: PathBuf,
    /// Why it did not load.
    pub error: Error,
}

/// One website a module file declares, ready to open for a scrape.
#[derive(Clone, Debug, PartialEq)]
pub struct Site {
    module: Module,
    /// The position of this declaration among those its file makes.
    declaration: usize,
    lua_dir: PathBuf,
}

impl Catalog {
    /// Loads every module in the `modules` directory under `lua_dir`, the
    /// `lua` directory of an FMD2 checkout.
    ///
    /// A module that fails to load is recorded in [`failures`](Self::failures)
    /// rather than failing the whole catalog.
    ///
    /// # Errors
    ///
    /// Fails if the `modules` directory cannot be read.
    pub fn load(lua_dir: impl Into<PathBuf>) -> Result<Self> {
        let lua_dir = lua_dir.into();
        let mut catalog = Self {
            sites: Vec::new(),
            failures: Vec::new(),
        };
        for file in module_files(&lua_dir)? {
            match Self::load_file(&lua_dir, &file) {
                Ok(sites) => catalog.sites.extend(sites),
                Err(error) => catalog.failures.push(LoadFailure { file, error }),
            }
        }
        Ok(catalog)
    }

    /// Loads the websites one module file declares, in declaration order.
    ///
    /// # Errors
    ///
    /// Fails if the file does not load, `Init()` raises, or it declares
    /// nothing.
    pub fn load_file(lua_dir: &Path, file: &Path) -> Result<Vec<Site>> {
        let runtime = Runtime::new(lua_dir, &SessionConfig::offline(), CancelToken::new())?;
        let modules = runtime.load(file)?;
        let sites = modules
            .into_iter()
            .enumerate()
            .map(|(declaration, module)| Site {
                module,
                declaration,
                lua_dir: lua_dir.to_path_buf(),
            });
        Ok(sites.collect())
    }

    /// Returns every website, in file order and then declaration order.
    #[must_use]
    pub fn sites(&self) -> &[Site] {
        &self.sites
    }

    /// Returns the module files that did not load.
    #[must_use]
    pub fn failures(&self) -> &[LoadFailure] {
        &self.failures
    }

    /// Returns the website named `name`, ignoring case.
    ///
    /// Mirrors of a site share its name, so `root_url` picks between them. An
    /// address no mirror declares is one the reader typed for a site that
    /// moved: the first mirror is returned, pointed at that address.
    #[must_use]
    pub fn site(&self, name: &str, root_url: Option<&str>) -> Option<Site> {
        select(&self.sites, name, root_url)
    }
}

impl Site {
    /// Returns the website as its module declared it.
    #[must_use]
    pub fn module(&self) -> &Module {
        &self.module
    }

    /// Returns this website pointed at `root_url`, for a site that moved.
    #[must_use]
    pub fn at(mut self, root_url: &str) -> Self {
        root_url.clone_into(&mut self.module.root_url);
        self
    }

    /// Loads the module again in a fresh runtime and opens a session for one
    /// scrape of this website.
    ///
    /// # Errors
    ///
    /// Fails if the module no longer loads, no longer declares this website,
    /// or `cancel` is already cancelled.
    pub fn open(&self, config: &SessionConfig, cancel: CancelToken) -> Result<Session> {
        let runtime = Runtime::new(&self.lua_dir, config, cancel)?;
        let declared = runtime.load(&self.module.file)?;
        let Some(module) = declared.into_iter().nth(self.declaration) else {
            return Err(self.changed());
        };
        if !module.name.eq_ignore_ascii_case(&self.module.name) {
            return Err(self.changed());
        }
        let module = Module {
            root_url: self.module.root_url.clone(),
            ..module
        };
        Session::new(runtime, module)
    }

    fn changed(&self) -> Error {
        let message = format!(
            "{} no longer declares {}; reload the catalog",
            self.module.file.display(),
            self.module.name
        );
        Error::new(ErrorKind::Load, message)
    }
}

/// Picks a website by name, then by address among mirrors.
pub(crate) fn select(sites: &[Site], name: &str, root_url: Option<&str>) -> Option<Site> {
    let mut matching = sites
        .iter()
        .filter(|s| s.module.name.eq_ignore_ascii_case(name));
    let first = matching.next()?;
    let Some(root_url) = root_url else {
        return Some(first.clone());
    };
    let mirror = std::iter::once(first)
        .chain(matching)
        .find(|s| s.module.root_url.eq_ignore_ascii_case(root_url));
    Some(mirror.map_or_else(|| first.clone().at(root_url), Clone::clone))
}

/// Lists the `.lua` files in the checkout's `modules` directory, sorted.
fn module_files(lua_dir: &Path) -> Result<Vec<PathBuf>> {
    let modules = lua_dir.join("modules");
    let unreadable =
        |e: std::io::Error| Error::new(ErrorKind::Load, format!("{}: {e}", modules.display()));
    let mut files = Vec::new();
    for entry in std::fs::read_dir(&modules).map_err(unreadable)? {
        let path = entry.map_err(unreadable)?.path();
        if path.extension().is_some_and(|ext| ext == "lua") {
            files.push(path);
        }
    }
    files.sort();
    Ok(files)
}

#[cfg(test)]
mod tests;
