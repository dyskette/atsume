//! Puts the tiles of a scrambled image back in place.

use std::io::Cursor;

use image::codecs::jpeg::JpegEncoder;
use image::{DynamicImage, ImageError, ImageFormat, ImageReader, Rgba, RgbaImage, imageops};

/// The JPEG quality that a descrambled JPEG is saved at.
///
/// Moving tiles happens in pixel space, so a JPEG loses one generation of
/// quality. At 95 the loss stays below what a reader notices. Upstream saves
/// at its toolkit's default, which is visibly worse.
const JPEG_QUALITY: u8 = 95;

/// The most tiles a grid may have. Real sites cut a page into a few dozen
/// tiles, and the limit keeps a mistaken grid size from exhausting memory.
pub(super) const MAX_TILES: i64 = 1 << 20;

/// Why a puzzle could not be built or an image could not be descrambled.
#[derive(Debug, thiserror::Error)]
pub(super) enum PuzzleError {
    #[error("a {columns}x{rows} grid has more than {MAX_TILES} tiles")]
    TooManyTiles { columns: i64, rows: i64 },
    #[error("the grid is {columns}x{rows}")]
    EmptyGrid { columns: i64, rows: i64 },
    #[error("matrix[{tile}] = {destination} is out of range 0..{last}")]
    DestinationOutOfRange {
        tile: usize,
        destination: i64,
        last: usize,
    },
    #[error("tiles would be {width}x{height} pixels in a {image_width}x{image_height} image")]
    TilesTooSmall {
        width: u32,
        height: u32,
        image_width: u32,
        image_height: u32,
    },
    #[error("decode: {0}")]
    Decode(ImageError),
    #[error("encode: {0}")]
    Encode(ImageError),
}

/// An image grid whose tiles a site has shuffled, and the permutation that
/// puts them back.
///
/// Tiles are numbered from 0, left to right and then top to bottom. Modules
/// build one with `fmd.imagepuzzle.Create`.
#[derive(Debug)]
pub(super) struct TilePuzzle {
    /// How many tiles each row has. Modules read it as `HorBlock`.
    pub(super) columns: i64,
    /// How many rows of tiles there are. Modules read it as `VerBlock`.
    pub(super) rows: i64,
    /// The number that each tile's width and height are rounded down to a
    /// multiple of, when it is above 1. Some sites cut on a coarser grid than
    /// the image's size implies. Modules set it through `Multiply`.
    pub(super) tile_size_multiple: i64,
    /// The destination of each scrambled tile, by the tile's number. Modules
    /// set it through `Matrix`.
    pub(super) destinations: Vec<i64>,
}

impl TilePuzzle {
    /// Builds a puzzle over a `columns` by `rows` grid that leaves every tile
    /// where it is.
    ///
    /// A grid with no tiles, or a negative size, gets an empty permutation,
    /// and descrambling with it fails.
    ///
    /// # Errors
    ///
    /// Fails when the grid has more than [`MAX_TILES`] tiles.
    pub(super) fn new(columns: i64, rows: i64) -> Result<Self, PuzzleError> {
        let tiles = match columns.checked_mul(rows) {
            Some(tiles) if tiles <= MAX_TILES => tiles.max(0),
            _ => return Err(PuzzleError::TooManyTiles { columns, rows }),
        };
        Ok(Self {
            columns,
            rows,
            tile_size_multiple: 1,
            destinations: (0..tiles).collect(),
        })
    }

    /// Reassembles the image in `scrambled` and returns it encoded again
    /// (`DeScramble`).
    ///
    /// A PNG or a WebP comes back as a PNG, and any other format comes back
    /// as a JPEG, as upstream does. Pixels that no tile covers, at the right
    /// and bottom edges of an image that the grid does not divide evenly, are
    /// opaque white.
    ///
    /// # Errors
    ///
    /// Fails when the grid is empty, when a destination is not a tile of the
    /// grid, when the image is too small to cut into the grid, or when the
    /// image cannot be decoded.
    pub(super) fn descramble(&self, scrambled: &[u8]) -> Result<Vec<u8>, PuzzleError> {
        let (columns, rows) = self.grid()?;
        let (format, source) = decode(scrambled)?;
        let (width, height) = source.dimensions();
        let tile_width = tile_length(width, columns, self.tile_size_multiple);
        let tile_height = tile_length(height, rows, self.tile_size_multiple);
        if tile_width == 0 || tile_height == 0 {
            return Err(PuzzleError::TilesTooSmall {
                width: tile_width,
                height: tile_height,
                image_width: width,
                image_height: height,
            });
        }

        let mut target = RgbaImage::from_pixel(width, height, Rgba([255; 4]));
        for (tile, &destination) in self.destinations.iter().enumerate() {
            let destination = self.checked_destination(tile, destination)?;
            let (source_x, source_y) = corner(tile, columns, tile_width, tile_height);
            let (target_x, target_y) = corner(destination, columns, tile_width, tile_height);
            let piece = imageops::crop_imm(&source, source_x, source_y, tile_width, tile_height);
            imageops::replace(
                &mut target,
                &*piece,
                i64::from(target_x),
                i64::from(target_y),
            );
        }
        encode(target, format)
    }

    /// Returns the grid's size, or an error when it has no tiles.
    fn grid(&self) -> Result<(u32, u32), PuzzleError> {
        let empty = || PuzzleError::EmptyGrid {
            columns: self.columns,
            rows: self.rows,
        };
        let columns = u32::try_from(self.columns).map_err(|_| empty())?;
        let rows = u32::try_from(self.rows).map_err(|_| empty())?;
        if columns == 0 || rows == 0 {
            return Err(empty());
        }
        Ok((columns, rows))
    }

    /// Returns `destination` as a tile number, or an error when the grid has
    /// no such tile.
    fn checked_destination(&self, tile: usize, destination: i64) -> Result<usize, PuzzleError> {
        let tiles = self.destinations.len();
        usize::try_from(destination)
            .ok()
            .filter(|&destination| destination < tiles)
            .ok_or(PuzzleError::DestinationOutOfRange {
                tile,
                destination,
                last: tiles.saturating_sub(1),
            })
    }
}

/// Decodes an image, whatever its format, and reports which format it was.
fn decode(bytes: &[u8]) -> Result<(Option<ImageFormat>, RgbaImage), PuzzleError> {
    let reader = ImageReader::new(Cursor::new(bytes))
        .with_guessed_format()
        .map_err(|error| PuzzleError::Decode(ImageError::IoError(error)))?;
    let format = reader.format();
    let image = reader.decode().map_err(PuzzleError::Decode)?;
    Ok((format, image.into_rgba8()))
}

/// Encodes `image` as a PNG when the scrambled image was a PNG or a WebP, and
/// as a JPEG otherwise.
fn encode(image: RgbaImage, scrambled_format: Option<ImageFormat>) -> Result<Vec<u8>, PuzzleError> {
    let mut encoded = Vec::new();
    if let Some(ImageFormat::Png | ImageFormat::WebP) = scrambled_format {
        image
            .write_to(&mut Cursor::new(&mut encoded), ImageFormat::Png)
            .map_err(PuzzleError::Encode)?;
        return Ok(encoded);
    }
    // JPEG has no alpha channel.
    let opaque = DynamicImage::ImageRgba8(image).into_rgb8();
    JpegEncoder::new_with_quality(&mut encoded, JPEG_QUALITY)
        .encode_image(&opaque)
        .map_err(PuzzleError::Encode)?;
    Ok(encoded)
}

/// Splits `length` pixels into `tiles` equal tiles and returns one tile's
/// length, rounded down to a multiple of `multiple` when `multiple` is above
/// 1.
pub(super) fn tile_length(length: u32, tiles: u32, multiple: i64) -> u32 {
    if multiple <= 1 {
        return length / tiles;
    }
    let multiple = u32::try_from(multiple).unwrap_or(u32::MAX);
    length / tiles.saturating_mul(multiple) * multiple
}

/// Returns the top-left pixel of tile number `tile`.
///
/// The grid fits inside the image, so the coordinates fit in a `u32`.
fn corner(tile: usize, columns: u32, tile_width: u32, tile_height: u32) -> (u32, u32) {
    let columns = columns as usize;
    let column = u32::try_from(tile % columns).unwrap_or(u32::MAX);
    let row = u32::try_from(tile / columns).unwrap_or(u32::MAX);
    (
        column.saturating_mul(tile_width),
        row.saturating_mul(tile_height),
    )
}
