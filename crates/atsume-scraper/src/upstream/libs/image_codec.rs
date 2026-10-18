//! Decodes the images that libraries rewrite, and encodes the results.

use std::io::Cursor;

use image::codecs::jpeg::JpegEncoder;
use image::{DynamicImage, ImageError, ImageFormat, ImageReader};

/// The quality that a rewritten JPEG is saved at.
///
/// Libraries move or crop pixels, so a JPEG they rewrite loses one generation
/// of quality. At 95 the loss stays below what a reader notices. Upstream
/// saves at its toolkit's default, which is visibly worse.
const JPEG_QUALITY: u8 = 95;

/// Decodes an image and reports which format it was in. The format is read
/// from the bytes themselves, because sites often serve one format under
/// another's name.
///
/// # Errors
///
/// Fails when the bytes are not an image in a format that atsume decodes.
pub(super) fn decode_with_format(
    bytes: &[u8],
) -> Result<(Option<ImageFormat>, DynamicImage), ImageError> {
    let reader = ImageReader::new(Cursor::new(bytes)).with_guessed_format()?;
    let format = reader.format();
    Ok((format, reader.decode()?))
}

/// Encodes `image` as a PNG.
///
/// # Errors
///
/// Fails if the PNG encoder reports an error, which in practice means an image
/// too large to encode.
pub(super) fn encode_png(image: &DynamicImage) -> Result<Vec<u8>, ImageError> {
    let mut encoded = Vec::new();
    image.write_to(&mut Cursor::new(&mut encoded), ImageFormat::Png)?;
    Ok(encoded)
}

/// Encodes `image` as a JPEG at [`JPEG_QUALITY`].
///
/// A grayscale image stays grayscale, which keeps the file smaller and the
/// page gray. Any other image is saved as 8-bit color, because JPEG has no
/// alpha channel.
///
/// # Errors
///
/// Fails if the JPEG encoder reports an error, which in practice means an image
/// larger than JPEG allows.
pub(super) fn encode_jpeg(image: &DynamicImage) -> Result<Vec<u8>, ImageError> {
    let mut encoded = Vec::new();
    let mut jpeg = JpegEncoder::new_with_quality(&mut encoded, JPEG_QUALITY);
    match image {
        DynamicImage::ImageLuma8(grey) => jpeg.encode_image(grey)?,
        other => jpeg.encode_image(&other.to_rgb8())?,
    }
    Ok(encoded)
}
