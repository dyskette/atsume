//! The banner templates that a page is matched against, and the crop that
//! removes a matched banner.
//!
//! One site stamps a banner across the bottom of every page, below the
//! artwork. Upstream ships images of that banner. A page whose bottom strip
//! matches one of them has the banner's height cut off its bottom.

use std::collections::BTreeMap;
use std::path::{Path, PathBuf};
use std::sync::{Arc, Mutex, PoisonError};

use image::{ImageError, ImageFormat, RgbaImage};

use super::one_bit_image::{Area, OneBitImage};
use crate::upstream::libs::image_codec::{decode_with_format, encode_jpeg, encode_png};

/// How alike, in decibels, a page's bottom strip and a template must be for
/// the strip to count as the banner. The value is upstream's. A lower value
/// cuts artwork off pages, and a higher one leaves banners in place.
const MIN_PEAK_SIGNAL_TO_NOISE: f64 = 9.0;

/// How many rows at the top of a strip must be white for the strip to count
/// as the banner. A white gap separates the banner from the artwork, so a
/// strip with ink in its top rows is artwork, however well it scores.
const WHITE_GAP_ROWS: u32 = 4;

/// The template sets loaded so far, by their directory's canonical path.
/// Modules load templates in `Init`, which runs for every scrape, so each
/// directory is read once for the life of the process.
static TEMPLATE_CACHE: Mutex<BTreeMap<PathBuf, Arc<BannerTemplates>>> = Mutex::new(BTreeMap::new());

/// Why a banner could not be removed from a page.
#[derive(Debug, thiserror::Error)]
pub(super) enum RemovalError {
    #[error("{}: {source}", path.display())]
    File {
        path: PathBuf,
        source: std::io::Error,
    },
    #[error("{}: decode: {source}", path.display())]
    Decode { path: PathBuf, source: ImageError },
    #[error("{}: encode: {source}", path.display())]
    Encode { path: PathBuf, source: ImageError },
}

/// The images of the banner, each reduced to black and white.
#[derive(Debug)]
pub(super) struct BannerTemplates {
    templates: Vec<OneBitImage>,
}

impl BannerTemplates {
    /// Returns the templates in the directory `dir`, which must be a
    /// canonical path, reading the directory only the first time it is asked
    /// for.
    ///
    /// # Errors
    ///
    /// Fails when the directory cannot be listed.
    pub(super) fn cached_or_read(dir: &Path) -> std::io::Result<Arc<Self>> {
        let mut cache = TEMPLATE_CACHE
            .lock()
            .unwrap_or_else(PoisonError::into_inner);
        if let Some(templates) = cache.get(dir) {
            return Ok(Arc::clone(templates));
        }
        let templates = Arc::new(Self::read_dir(dir)?);
        cache.insert(dir.to_path_buf(), Arc::clone(&templates));
        Ok(templates)
    }

    /// Reads every image in the directory `dir`, which must be a canonical
    /// path, in the order of the file names.
    ///
    /// A file that is not an image is skipped, as upstream skips it. So is a
    /// symbolic link that leads out of the directory. The order decides only
    /// which of two equally good templates wins, and it is fixed so that every
    /// run picks the same one.
    ///
    /// # Errors
    ///
    /// Fails when the directory cannot be listed.
    pub(super) fn read_dir(dir: &Path) -> std::io::Result<Self> {
        let mut paths: Vec<PathBuf> = std::fs::read_dir(dir)?
            .filter_map(Result::ok)
            .map(|entry| entry.path())
            .collect();
        paths.sort();
        let templates = paths
            .iter()
            .filter_map(|path| read_template(dir, path))
            .collect();
        Ok(Self { templates })
    }

    /// Returns how many templates there are.
    pub(super) fn len(&self) -> usize {
        self.templates.len()
    }

    /// Cuts the banner off the bottom of the image file at `path`, and
    /// reports whether there was one (`RemoveWatermark`).
    ///
    /// The file is rewritten in place. When `as_png` is set, the result is
    /// saved as a PNG beside it under the extension `.png`, and the original
    /// is deleted. Otherwise a PNG stays a PNG and any other image becomes a
    /// JPEG. A page without a banner is left untouched.
    ///
    /// # Errors
    ///
    /// Fails when the file cannot be read or written, or is not an image.
    pub(super) fn remove_banner_from_file(
        &self,
        path: &Path,
        as_png: bool,
    ) -> Result<bool, RemovalError> {
        if self.templates.is_empty() {
            return Ok(false);
        }
        let file_error = |source| RemovalError::File {
            path: path.to_path_buf(),
            source,
        };
        let bytes = std::fs::read(path).map_err(file_error)?;
        let (format, page) = decode_with_format(&bytes).map_err(|source| RemovalError::Decode {
            path: path.to_path_buf(),
            source,
        })?;
        let Some(kept_height) = self.height_above_banner(&page.to_rgba8()) else {
            return Ok(false);
        };

        let cropped = page.crop_imm(0, 0, page.width(), kept_height);
        let save_as_png = as_png || format == Some(ImageFormat::Png);
        let encoded = if save_as_png {
            encode_png(&cropped)
        } else {
            encode_jpeg(&cropped)
        };
        let encoded = encoded.map_err(|source| RemovalError::Encode {
            path: path.to_path_buf(),
            source,
        })?;
        let target = if as_png {
            path.with_extension("png")
        } else {
            path.to_path_buf()
        };
        std::fs::write(&target, encoded).map_err(file_error)?;
        if target != path {
            remove_replaced_original(path);
        }
        Ok(true)
    }

    /// Returns how many rows of `page` lie above its banner, or `None` when
    /// the page has no banner.
    ///
    /// A template as tall as the whole page also gives `None`, because an
    /// empty page would be worse than one that keeps its banner.
    fn height_above_banner(&self, page: &RgbaImage) -> Option<u32> {
        let (template, score) = self.best_match(page)?;
        if score < MIN_PEAK_SIGNAL_TO_NOISE {
            return None;
        }
        let kept_height = page.height() - template.height;
        (kept_height > 0).then_some(kept_height)
    }

    /// Returns the template that best matches the bottom of `page`, and its
    /// score.
    ///
    /// Only the bottom strip is compared, because the banner is stamped there,
    /// and matching anywhere else would compare artwork with the banner.
    fn best_match(&self, page: &RgbaImage) -> Option<(&OneBitImage, f64)> {
        let mut best = None;
        let mut best_score = 0.0;
        for template in &self.templates {
            let Some(score) = score_bottom_strip(page, template) else {
                continue;
            };
            if score > best_score {
                (best, best_score) = (Some(template), score);
            }
        }
        best.map(|template| (template, best_score))
    }
}

/// Reads the template at `path`, or returns `None` when it is not an image or
/// leads out of the directory `dir`.
fn read_template(dir: &Path, path: &Path) -> Option<OneBitImage> {
    let path = path.canonicalize().ok()?;
    if !path.starts_with(dir) || !path.is_file() {
        return None;
    }
    let image = image::load_from_memory(&std::fs::read(&path).ok()?).ok()?;
    let image = image.into_rgba8();
    Some(OneBitImage::threshold_area(&image, Area::whole(&image)))
}

/// Scores how well the bottom of `page` matches `template`, or returns `None`
/// when the page is shorter than the template or the strip has ink in its
/// top rows.
///
/// The strip is as wide as the template and centred on the page.
fn score_bottom_strip(page: &RgbaImage, template: &OneBitImage) -> Option<f64> {
    let top = page.height().checked_sub(template.height)?;
    let strip = Area {
        left: page.width().saturating_sub(template.width) / 2,
        top,
        width: template.width,
        height: template.height,
    };
    let strip = OneBitImage::threshold_area(page, strip);
    if !strip.top_rows_are_white(WHITE_GAP_ROWS) {
        return None;
    }
    Some(strip.peak_signal_to_noise_ratio(template))
}

/// Deletes the original page once its replacement under another extension is
/// written. A failure leaves both files, so it is logged and not raised.
fn remove_replaced_original(path: &Path) {
    if let Err(error) = std::fs::remove_file(path) {
        tracing::warn!(path = %path.display(), %error, "could not delete the page that a PNG replaced");
    }
}
