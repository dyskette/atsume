//! Puts the tiles of a scrambled image back in place.

use image::{DynamicImage, ImageError, ImageFormat, Rgba, RgbaImage, imageops};

use crate::upstream::libs::image_codec::{decode_with_format, encode_jpeg, encode_png};

/// The most tiles a grid may have. Real sites cut a page into a few dozen
/// tiles, and the limit keeps a mistaken grid size from exhausting memory.
pub(super) const MAX_TILES: i64 = 1 << 20;

/// Why a puzzle could not be built or an image could not be descrambled.
#[derive(Debug, thiserror::Error)]
pub(super) enum TilePuzzleError {
    #[error("a {columns}x{rows} grid has more than {MAX_TILES} tiles")]
    TooManyTiles { columns: i64, rows: i64 },
    #[error("the grid is {columns}x{rows}, so it has no tiles")]
    EmptyGrid { columns: i64, rows: i64 },
    #[error("Matrix[{tile}] = {destination} is not a tile; the grid has tiles 0 to {last}")]
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
/// build one with `fmd.imagepuzzle.Create`, and each field's doc comment gives,
/// in parentheses, the name through which modules reach it.
#[derive(Debug)]
pub(super) struct TilePuzzle {
    /// How many tiles each row has (`HorBlock`).
    pub(super) columns: i64,
    /// How many rows of tiles there are (`VerBlock`).
    pub(super) rows: i64,
    /// The step that tile sizes are rounded down to (`Multiply`). When the
    /// step is above 1, each tile's width and height become the largest
    /// multiple of the step that fits. Some sites cut on a coarser grid than
    /// the image's size implies.
    pub(super) tile_size_multiple: i64,
    /// The destination of each scrambled tile, by the tile's number
    /// (`Matrix`).
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
    pub(super) fn new(columns: i64, rows: i64) -> Result<Self, TilePuzzleError> {
        let tiles = match columns.checked_mul(rows) {
            Some(tiles) if tiles <= MAX_TILES => tiles.max(0),
            _ => return Err(TilePuzzleError::TooManyTiles { columns, rows }),
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
    pub(super) fn descramble(&self, scrambled: &[u8]) -> Result<Vec<u8>, TilePuzzleError> {
        let (columns, rows) = self.nonempty_grid()?;
        let (format, source) = decode(scrambled)?;
        let (width, height) = source.dimensions();
        let tile_width = tile_length(width, columns, self.tile_size_multiple);
        let tile_height = tile_length(height, rows, self.tile_size_multiple);
        if tile_width == 0 || tile_height == 0 {
            return Err(TilePuzzleError::TilesTooSmall {
                width: tile_width,
                height: tile_height,
                image_width: width,
                image_height: height,
            });
        }

        let mut target = RgbaImage::from_pixel(width, height, Rgba([255; 4]));
        for (tile, &destination) in self.destinations.iter().enumerate() {
            let destination = self.checked_destination(tile, destination)?;
            let (source_x, source_y) = tile_corner(tile, columns, tile_width, tile_height);
            let (target_x, target_y) = tile_corner(destination, columns, tile_width, tile_height);
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
    fn nonempty_grid(&self) -> Result<(u32, u32), TilePuzzleError> {
        let empty = || TilePuzzleError::EmptyGrid {
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
    fn checked_destination(&self, tile: usize, destination: i64) -> Result<usize, TilePuzzleError> {
        let tiles = self.destinations.len();
        usize::try_from(destination)
            .ok()
            .filter(|&destination| destination < tiles)
            .ok_or(TilePuzzleError::DestinationOutOfRange {
                tile,
                destination,
                last: tiles.saturating_sub(1),
            })
    }
}

/// Decodes an image, whatever its format, and reports which format it was.
fn decode(bytes: &[u8]) -> Result<(Option<ImageFormat>, RgbaImage), TilePuzzleError> {
    let (format, image) = decode_with_format(bytes).map_err(TilePuzzleError::Decode)?;
    Ok((format, image.into_rgba8()))
}

/// Encodes `image` as a PNG when the scrambled image was a PNG or a WebP, and
/// as a JPEG otherwise.
fn encode(
    image: RgbaImage,
    scrambled_format: Option<ImageFormat>,
) -> Result<Vec<u8>, TilePuzzleError> {
    let image = DynamicImage::ImageRgba8(image);
    let encoded = match scrambled_format {
        Some(ImageFormat::Png | ImageFormat::WebP) => encode_png(&image),
        _ => encode_jpeg(&image),
    };
    encoded.map_err(TilePuzzleError::Encode)
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
/// The grid fits inside the image, so the coordinates always fit in a `u32`.
/// The fallbacks in the conversions never apply; they are there because the
/// types alone cannot show that.
fn tile_corner(tile: usize, columns: u32, tile_width: u32, tile_height: u32) -> (u32, u32) {
    let columns = columns as usize;
    let column = u32::try_from(tile % columns).unwrap_or(u32::MAX);
    let row = u32::try_from(tile / columns).unwrap_or(u32::MAX);
    (
        column.saturating_mul(tile_width),
        row.saturating_mul(tile_height),
    )
}
