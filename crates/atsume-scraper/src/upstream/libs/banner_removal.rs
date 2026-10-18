//! Removes the banner that MangaFox stamps across the bottom of its pages.
//! Modules load this library as `fmd.mangafoxwatermark`, which is FMD2's name
//! for it.
//!
//! A module loads the banner templates in `Init` with `LoadTemplate`, and
//! calls `RemoveWatermark` from `OnAfterImageSaved` on the page just saved.

mod banner_templates;
mod one_bit_image;
#[cfg(test)]
mod tests;

use std::cell::RefCell;
use std::path::{Path, PathBuf};
use std::rc::Rc;
use std::sync::Arc;

use mlua::{Lua, Table, Value};

use crate::runtime::bind::borrow_state;
use crate::runtime::convert::{text_arg, truthy};
use crate::runtime::sandbox::{WritableFile, resolve_in_root};

use banner_templates::BannerTemplates;

/// The templates that one copy of the library loaded last, if any.
///
/// Each runtime builds its own copy of the library, so a module sees only the
/// templates that it loaded itself.
type CurrentTemplates = Rc<RefCell<Option<Arc<BannerTemplates>>>>;

/// Builds the `fmd.mangafoxwatermark` table. `root` is the checkout's root,
/// the only place that templates are read from.
pub(super) fn build(lua: &Lua, root: PathBuf) -> mlua::Result<Table> {
    let library = lua.create_table()?;
    let current_for_remove = CurrentTemplates::default();
    let current_for_load = Rc::clone(&current_for_remove);
    let load = lua.create_function(move |lua, dir: Value| {
        let dir = text_arg(lua, &dir, 1)?;
        Ok(load_templates(&root, &dir, &current_for_load))
    })?;
    let remove = lua.create_function(move |lua, (path, as_png): (Value, Value)| {
        let path = text_arg(lua, &path, 1)?;
        remove_banner(lua, &current_for_remove, &path, truthy(&as_png))
    })?;
    library.set("LoadTemplate", load)?;
    library.set("RemoveWatermark", remove)?;
    Ok(library)
}

/// Loads the templates in the directory `dir` and returns how many there are
/// (`LoadTemplate`).
///
/// The directory is resolved against the checkout's root `root`, and a path
/// with backslashes, which modules written for Windows build, works on every
/// system. A directory that cannot be read, or that lies outside the checkout,
/// gives 0 and keeps the templates loaded before, as upstream does. The
/// failure is logged, because otherwise it looks like a page without a
/// banner.
fn load_templates(root: &Path, dir: &str, current: &CurrentTemplates) -> usize {
    let dir = dir.replace('\\', "/");
    let templates = resolve_in_root(root, &dir).and_then(|dir| {
        BannerTemplates::cached_or_read(&dir).map_err(|e| format!("{}: {e}", dir.display()))
    });
    let templates = match templates {
        Ok(templates) => templates,
        Err(error) => {
            tracing::warn!(%dir, %error, "no MangaFox watermark templates were loaded");
            return 0;
        }
    };
    if templates.len() == 0 {
        tracing::warn!(%dir, "the MangaFox watermark template directory holds no images");
    }
    let count = templates.len();
    current.replace(Some(templates));
    count
}

/// Cuts the banner off the page at `path` and reports whether there was one
/// (`RemoveWatermark`).
///
/// Only the page that `OnAfterImageSaved` is editing may be changed, because
/// the call rewrites the file and may delete it. When no templates are
/// loaded, the page is left alone and the call returns false.
///
/// # Errors
///
/// Fails when `path` is not the page being saved, or when the page cannot be
/// read, decoded or written.
fn remove_banner(
    lua: &Lua,
    current: &CurrentTemplates,
    path: &str,
    as_png: bool,
) -> mlua::Result<bool> {
    if !borrow_state::<WritableFile>(lua)?.allows(path) {
        return Err(mlua::Error::runtime(format!(
            "fmd.mangafoxwatermark.RemoveWatermark: {path} is not the page being saved"
        )));
    }
    let Some(templates) = current.borrow().clone() else {
        return Ok(false);
    };
    templates
        .remove_banner_from_file(Path::new(path), as_png)
        .map_err(|error| {
            mlua::Error::runtime(format!("fmd.mangafoxwatermark.RemoveWatermark: {error}"))
        })
}
