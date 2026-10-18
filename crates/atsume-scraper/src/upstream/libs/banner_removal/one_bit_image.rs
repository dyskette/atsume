//! Black-and-white images, the form in which banner templates and the bottom
//! of a page are compared.

use image::{Rgba, RgbaImage};

/// The value of a black pixel.
const BLACK: u8 = 0;

/// The value of a white pixel.
const WHITE: u8 = u8::MAX;

/// How many gray levels an 8-bit pixel can take.
const GRAY_LEVELS: usize = 256;

/// The score of two identical images. A peak signal-to-noise ratio is infinite
/// for identical images, and this finite value stands for that.
const IDENTICAL_SCORE: f64 = 1e6;

/// The root mean squared error below which two images count as identical.
const IDENTICAL_ERROR: f64 = 0.0001;

/// The Rec. 601 weights of red, green and blue in a pixel's brightness, which
/// are 0.299, 0.587 and 0.114, written as fractions of 2^[`WEIGHT_BITS`]. They
/// sum to exactly 2^16, so a white pixel stays white.
const REC_601_WEIGHTS: [u32; 3] = [19_595, 38_470, 7_471];

/// How many fractional bits the color weights carry.
const WEIGHT_BITS: u32 = 16;

/// Widens an 8-bit channel to 16 bits, so that 255 becomes 65535.
const WIDEN_TO_16_BITS: u32 = 257;

/// How many bits a 16-bit channel drops to become an 8-bit one.
const NARROW_TO_8_BITS: u32 = 8;

/// The rounding term that upstream's brightness arithmetic adds before it
/// shifts the result down.
const BRIGHTNESS_ROUNDING: u32 = 1 << 15;

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

/// A grayscale image reduced to black and white by Otsu's method. Each pixel
/// is [`BLACK`] or [`WHITE`].
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
    /// centered in it, so that a page narrower than a template can still be
    /// compared with the template. Any part of the area below the image is
    /// white too.
    pub(super) fn threshold_area(image: &RgbaImage, area: Area) -> Self {
        let width = area.width as usize;
        let mut pixels = vec![WHITE; width * area.height as usize];
        let padding = area.width.saturating_sub(image.width()) / 2;
        for (row, target) in (area.top..image.height()).zip(pixels.chunks_exact_mut(width)) {
            let target = &mut target[padding as usize..];
            copy_row_as_gray(image, row, area.left, target);
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
        self.pixels[..pixels].iter().all(|&pixel| pixel == WHITE)
    }

    /// Measures how alike this image and `other` are, as a peak
    /// signal-to-noise ratio in decibels. Higher is more alike.
    ///
    /// Two images of different sizes, or two empty images, score 0. Two
    /// identical images score [`IDENTICAL_SCORE`].
    pub(super) fn peak_signal_to_noise_ratio(&self, other: &Self) -> f64 {
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
        if mean_squared_error.sqrt() < IDENTICAL_ERROR {
            return IDENTICAL_SCORE;
        }
        let peak = f64::from(WHITE);
        // Ten times the base-10 logarithm turns a power ratio into decibels.
        10.0 * (peak * peak / mean_squared_error).log10()
    }
}

/// Copies row `row` of `image`, from column `left` on, into `target` as
/// grayscale. The copy stops at the end of `target` or of the image's row,
/// whichever comes first.
fn copy_row_as_gray(image: &RgbaImage, row: u32, left: u32, target: &mut [u8]) {
    for (column, gray) in (left..image.width()).zip(target) {
        *gray = luma(*image.get_pixel(column, row));
    }
}

/// Returns a pixel's brightness by the Rec. 601 weights.
///
/// The arithmetic runs on 16-bit channels whose color is premultiplied by
/// alpha, and then drops to 8 bits. Upstream computes brightness this way, and
/// its templates are matched against brightness computed so. One consequence
/// is that a transparent pixel reads as black.
fn luma(pixel: Rgba<u8>) -> u8 {
    let [red, green, blue, alpha] = pixel.0;
    let premultiplied =
        |channel: u8| u32::from(channel) * WIDEN_TO_16_BITS * u32::from(alpha) / u32::from(WHITE);
    let [red_weight, green_weight, blue_weight] = REC_601_WEIGHTS;
    let weighted = red_weight * premultiplied(red)
        + green_weight * premultiplied(green)
        + blue_weight * premultiplied(blue);
    let luma = (weighted + BRIGHTNESS_ROUNDING) >> (WEIGHT_BITS + NARROW_TO_8_BITS);
    u8::try_from(luma).unwrap_or(WHITE)
}

/// Turns each pixel black or white at the threshold that Otsu's method picks.
///
/// The method tries every gray level and keeps the one that best separates
/// the pixels into a dark class and a light class. A pixel above the
/// threshold becomes white, and every other pixel becomes black.
fn apply_otsu_threshold(pixels: &mut [u8]) {
    if pixels.is_empty() {
        return;
    }
    let mut histogram = [0.0_f64; GRAY_LEVELS];
    for &pixel in pixels.iter() {
        histogram[usize::from(pixel)] += 1.0;
    }
    let threshold = otsu_threshold(&histogram);
    for pixel in pixels {
        *pixel = if *pixel > threshold { WHITE } else { BLACK };
    }
}

/// Picks the gray level that maximizes the variance between the pixels at or
/// below it and the pixels above it. `histogram` counts the pixels at each
/// level.
fn otsu_threshold(histogram: &[f64; GRAY_LEVELS]) -> u8 {
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
