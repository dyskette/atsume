//! Black-and-white images, the form in which banner templates and the bottom
//! of a page are compared.

use image::{Rgba, RgbaImage};

/// A rectangle of an image, given by its top-left pixel and its size.
#[derive(Clone, Copy, Debug)]
pub(super) struct Area {
    pub(super) left: u32,
    pub(super) top: u32,
    pub(super) width: u32,
    pub(super) height: u32,
}

impl Area {
    /// Returns the area that covers all of `image`.
    pub(super) fn whole(image: &RgbaImage) -> Self {
        Self {
            left: 0,
            top: 0,
            width: image.width(),
            height: image.height(),
        }
    }
}

/// A greyscale image reduced to black and white by Otsu's method. Each pixel
/// is 0 for black or 255 for white.
#[derive(Debug)]
pub(super) struct OneBitImage {
    pub(super) width: u32,
    pub(super) height: u32,
    /// The pixels, row by row from the top.
    pixels: Vec<u8>,
}

impl OneBitImage {
    /// Reduces `area` of `image` to black and white.
    ///
    /// An area wider than the image is padded with white, with the image
    /// centred in it, so that a page narrower than a template can still be
    /// compared with the template. Any part of the area below the image is
    /// white too.
    pub(super) fn from_area(image: &RgbaImage, area: Area) -> Self {
        let width = area.width as usize;
        let mut pixels = vec![u8::MAX; width * area.height as usize];
        let padding = area.width.saturating_sub(image.width()) / 2;
        for (row, target) in (area.top..image.height()).zip(pixels.chunks_exact_mut(width)) {
            let target = &mut target[padding as usize..];
            copy_row_as_grey(image, row, area.left, target);
        }
        apply_otsu_threshold(&mut pixels);
        Self {
            width: area.width,
            height: area.height,
            pixels,
        }
    }

    /// Reports whether the top `rows` rows hold only white pixels.
    pub(super) fn top_rows_are_white(&self, rows: u32) -> bool {
        let pixels = (rows as usize * self.width as usize).min(self.pixels.len());
        self.pixels[..pixels].iter().all(|&pixel| pixel != 0)
    }

    /// Measures how alike this image and `other` are, as a peak
    /// signal-to-noise ratio in decibels. Higher is more alike.
    ///
    /// Two images of different sizes, or two empty images, score 0. Two
    /// identical images score 1,000,000, which stands for infinity.
    pub(super) fn peak_signal_to_noise(&self, other: &Self) -> f64 {
        if self.pixels.len() != other.pixels.len() || self.pixels.is_empty() {
            return 0.0;
        }
        let squared_error: f64 = self
            .pixels
            .iter()
            .zip(&other.pixels)
            .map(|(&a, &b)| {
                let difference = f64::from(b) - f64::from(a);
                difference * difference
            })
            .sum();
        let mean_squared_error = squared_error / pixel_count(self.pixels.len());
        if mean_squared_error.sqrt() < 0.0001 {
            return 1e6;
        }
        10.0 * (255.0 * 255.0 / mean_squared_error).log10()
    }
}

/// Copies row `row` of `image`, from column `left` on, into `target` as
/// greyscale. The copy stops at the end of `target` or of the image's row,
/// whichever comes first.
fn copy_row_as_grey(image: &RgbaImage, row: u32, left: u32, target: &mut [u8]) {
    for (column, grey) in (left..image.width()).zip(target) {
        *grey = luma(*image.get_pixel(column, row));
    }
}

/// Returns a pixel's brightness by the Rec. 601 weights.
///
/// The arithmetic runs on 16-bit channels whose colour is premultiplied by
/// alpha, and then drops to 8 bits. Upstream's templates were matched by that
/// arithmetic, so a transparent pixel reads as black.
fn luma(pixel: Rgba<u8>) -> u8 {
    let [red, green, blue, alpha] = pixel.0;
    let premultiplied = |channel: u8| u32::from(channel) * 257 * u32::from(alpha) / 255;
    let luma = (19595 * premultiplied(red)
        + 38470 * premultiplied(green)
        + 7471 * premultiplied(blue)
        + (1 << 15))
        >> 24;
    u8::try_from(luma).unwrap_or(u8::MAX)
}

/// Turns each pixel black or white at the threshold that Otsu's method picks.
///
/// The method tries every grey level and keeps the one that best separates
/// the pixels into a dark class and a light class. A pixel above the
/// threshold becomes white, and every other pixel becomes black.
fn apply_otsu_threshold(pixels: &mut [u8]) {
    if pixels.is_empty() {
        return;
    }
    let mut histogram = [0.0_f64; 256];
    for &pixel in pixels.iter() {
        histogram[usize::from(pixel)] += 1.0;
    }
    let threshold = otsu_threshold(&histogram);
    for pixel in pixels {
        *pixel = if *pixel > threshold { u8::MAX } else { 0 };
    }
}

/// Picks the grey level that maximises the variance between the pixels at or
/// below it and the pixels above it. `histogram` counts the pixels at each
/// level.
fn otsu_threshold(histogram: &[f64; 256]) -> u8 {
    let total: f64 = histogram.iter().sum();
    let mean: f64 = (0..=u8::MAX)
        .zip(histogram)
        .map(|(level, &count)| f64::from(level) * count / total)
        .sum();

    let (mut threshold, mut best) = (0, 0.0);
    let (mut weight, mut moment) = (0.0, 0.0);
    for (level, &count) in (0..=u8::MAX).zip(histogram) {
        weight += count / total;
        moment += f64::from(level) * count / total;
        // With every pixel on one side, there is nothing to separate.
        if weight <= 0.0 || weight >= 1.0 {
            continue;
        }
        let separation = mean * weight - moment;
        let between = separation * separation / (weight * (1.0 - weight));
        if between > best {
            (threshold, best) = (level, between);
        }
    }
    threshold
}

/// Converts a number of pixels to a float, for averaging.
#[expect(
    clippy::cast_precision_loss,
    reason = "an image has far fewer than 2^52 pixels, so the conversion is exact"
)]
fn pixel_count(pixels: usize) -> f64 {
    pixels as f64
}
