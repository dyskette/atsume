use std::io::Cursor;

use image::{DynamicImage, ImageFormat, Rgba, RgbaImage};
use mlua::Lua;

use super::tile_puzzle::{MAX_TILES, TilePuzzle, TilePuzzleError, tile_length};
use super::*;
use crate::CancelToken;
use crate::http::HttpConfig;

/// Builds an image whose tiles each have a different flat colour, so a tile
/// in the wrong place shows up as differing pixels.
fn grid_image(columns: u32, rows: u32, tile_width: u32, tile_height: u32) -> RgbaImage {
    RgbaImage::from_fn(columns * tile_width, rows * tile_height, |x, y| {
        let tile = (y / tile_height) * columns + x / tile_width;
        let tile = u8::try_from(tile).unwrap();
        Rgba([
            17u8.wrapping_mul(tile + 1),
            255u8.wrapping_sub(13u8.wrapping_mul(tile)),
            7u8.wrapping_mul(tile),
            255,
        ])
    })
}

/// Builds the image a site would serve. Descrambling moves tile `i` to
/// `destinations[i]`, so the scrambled tile `i` holds the original tile
/// `destinations[i]`.
fn scramble(
    original: &RgbaImage,
    columns: u32,
    tile_width: u32,
    tile_height: u32,
    destinations: &[i64],
) -> RgbaImage {
    let tile_of = |x: u32, y: u32| ((y / tile_height) * columns + x / tile_width) as usize;
    RgbaImage::from_fn(original.width(), original.height(), |x, y| {
        let destination = u32::try_from(destinations[tile_of(x, y)]).unwrap();
        let source_x = (destination % columns) * tile_width + x % tile_width;
        let source_y = (destination / columns) * tile_height + y % tile_height;
        *original.get_pixel(source_x, source_y)
    })
}

fn encode(image: &RgbaImage, format: ImageFormat) -> Vec<u8> {
    let mut image = DynamicImage::from(image.clone());
    if format == ImageFormat::Jpeg {
        // JPEG has no alpha channel.
        image = image.into_rgb8().into();
    }
    let mut encoded = Vec::new();
    image
        .write_to(&mut Cursor::new(&mut encoded), format)
        .unwrap();
    encoded
}

fn png(image: &RgbaImage) -> Vec<u8> {
    encode(image, ImageFormat::Png)
}

/// Decodes `bytes` and reports the format they were in.
fn decode(bytes: &[u8]) -> (ImageFormat, RgbaImage) {
    let format = image::guess_format(bytes).unwrap();
    (format, image::load_from_memory(bytes).unwrap().into_rgba8())
}

/// Checks that `actual` and `expected` have the same size and pixels, and
/// names the first pixel that differs.
fn assert_same_pixels(actual: &RgbaImage, expected: &RgbaImage, context: &str) {
    assert_eq!(actual.dimensions(), expected.dimensions(), "{context}");
    let differing = actual
        .enumerate_pixels()
        .find(|&(x, y, pixel)| pixel != expected.get_pixel(x, y));
    if let Some((x, y, _)) = differing {
        panic!("{context}: pixel ({x}, {y}) differs");
    }
}

/// Returns a permutation of `tiles` tiles that moves most of them.
fn shuffled(tiles: i64) -> Vec<i64> {
    (0..tiles)
        .map(|i| (tiles - 1 - i + tiles / 2) % tiles)
        .collect()
}

fn puzzle(columns: i64, rows: i64, destinations: &[i64]) -> TilePuzzle {
    let mut puzzle = TilePuzzle::new(columns, rows).unwrap();
    puzzle.destinations = destinations.to_vec();
    puzzle
}

#[test]
fn a_scrambled_image_reassembles_into_the_original() {
    for (columns, rows, tile_width, tile_height) in [
        (2, 2, 8, 8),
        (3, 3, 10, 10),
        (4, 4, 16, 16),
        (5, 3, 12, 20),
        (1, 1, 9, 9),
    ] {
        let destinations = shuffled(i64::from(columns * rows));
        let original = grid_image(columns, rows, tile_width, tile_height);
        let scrambled = scramble(&original, columns, tile_width, tile_height, &destinations);
        let descrambled = puzzle(columns.into(), rows.into(), &destinations)
            .descramble(&png(&scrambled))
            .unwrap();
        assert_same_pixels(
            &decode(&descrambled).1,
            &original,
            &format!("{columns}x{rows}"),
        );
    }
}

#[test]
fn a_new_puzzle_leaves_every_tile_in_place() {
    let original = grid_image(3, 3, 8, 8);
    let puzzle = TilePuzzle::new(3, 3).unwrap();
    assert_eq!(puzzle.destinations, (0..9).collect::<Vec<_>>());
    let descrambled = puzzle.descramble(&png(&original)).unwrap();
    assert_same_pixels(&decode(&descrambled).1, &original, "identity");
}

#[test]
fn png_and_webp_come_back_as_png_and_other_formats_as_jpeg() {
    let original = grid_image(2, 2, 8, 8);
    let puzzle = TilePuzzle::new(2, 2).unwrap();
    for (format, expected) in [
        (ImageFormat::Png, ImageFormat::Png),
        (ImageFormat::WebP, ImageFormat::Png),
        (ImageFormat::Jpeg, ImageFormat::Jpeg),
        (ImageFormat::Gif, ImageFormat::Jpeg),
    ] {
        let descrambled = puzzle.descramble(&encode(&original, format)).unwrap();
        assert_eq!(decode(&descrambled).0, expected, "{format:?}");
    }
}

#[test]
fn pixels_that_no_tile_covers_are_opaque_white() {
    let grey = RgbaImage::from_pixel(33, 33, Rgba([128; 4]));
    let descrambled = TilePuzzle::new(4, 4)
        .unwrap()
        .descramble(&png(&grey))
        .unwrap();
    let (_, image) = decode(&descrambled);
    assert_eq!(image.dimensions(), (33, 33));
    // A tile is 33 / 4 = 8 pixels wide, so the last column and row are left over.
    assert_eq!(*image.get_pixel(32, 32), Rgba([255; 4]));
    assert_eq!(*image.get_pixel(31, 31), Rgba([128; 4]));
}

#[test]
fn a_tile_size_multiple_rounds_each_tile_down() {
    // 100 / (4 * 8) = 3, and 3 * 8 = 24.
    assert_eq!(tile_length(100, 4, 8), 24);
    for multiple in [1, 0, -3] {
        assert_eq!(tile_length(100, 4, multiple), 25, "{multiple}");
    }
    assert_eq!(tile_length(100, 4, i64::MAX), 0);
}

#[test]
fn descrambling_reports_what_is_wrong() {
    let valid = png(&grid_image(2, 2, 4, 4));
    let failure = |puzzle: TilePuzzle, image: &[u8]| puzzle.descramble(image).unwrap_err();

    let out_of_range = failure(puzzle(2, 2, &[99, 1, 2, 3]), &valid);
    assert!(matches!(
        out_of_range,
        TilePuzzleError::DestinationOutOfRange {
            tile: 0,
            destination: 99,
            last: 3
        }
    ));
    let negative = failure(puzzle(2, 2, &[0, -1, 2, 3]), &valid);
    assert!(matches!(
        negative,
        TilePuzzleError::DestinationOutOfRange { tile: 1, .. }
    ));
    let not_an_image = failure(TilePuzzle::new(2, 2).unwrap(), b"not an image");
    assert!(matches!(not_an_image, TilePuzzleError::Decode(_)));
    for (columns, rows) in [(0, 0), (2, 0), (-1, -1)] {
        let empty = failure(TilePuzzle::new(columns, rows).unwrap(), &valid);
        assert!(
            matches!(empty, TilePuzzleError::EmptyGrid { .. }),
            "{columns}x{rows}"
        );
    }
    let too_fine = failure(TilePuzzle::new(16, 1).unwrap(), &valid);
    assert!(matches!(
        too_fine,
        TilePuzzleError::TilesTooSmall {
            width: 0,
            height: 8,
            ..
        }
    ));
}

#[test]
fn a_grid_with_too_many_tiles_is_refused() {
    assert!(TilePuzzle::new(MAX_TILES, 1).is_ok());
    assert!(matches!(
        TilePuzzle::new(MAX_TILES, 2),
        Err(TilePuzzleError::TooManyTiles { .. })
    ));
    assert!(TilePuzzle::new(i64::MAX, i64::MAX).is_err());
}

/// Runs `source` with `fmd.imagepuzzle` in the global `imagepuzzle` and with
/// `HTTP.Document`, holding `document`, in the global `DOC`. Returns what
/// `source` returns, and the document afterwards.
fn run(document: &[u8], source: &str) -> mlua::Result<(String, Vec<u8>)> {
    let lua = Lua::new();
    let mut http = HttpSession::new(&HttpConfig::default(), CancelToken::new());
    http.response_body = document.to_vec();
    lua.set_app_data(http);
    let doc = DocumentObject::<HttpSession>::new(|h| &mut h.response_body);
    lua.globals().set("imagepuzzle", build(&lua)?)?;
    lua.globals().set("DOC", doc)?;
    let result: Option<String> = lua
        .load(format!("local p = imagepuzzle.Create(2, 2)\n{source}"))
        .eval()?;
    Ok((result.unwrap_or_default(), doc.bytes(&lua)?))
}

/// Checks that each `(source, expected)` returns `expected`.
fn check(cases: &[(&str, &str)]) {
    for (source, expected) in cases {
        match run(b"", source) {
            Ok((got, _)) => assert_eq!(got, *expected, "{source}"),
            Err(error) => panic!("{source}: {error}"),
        }
    }
}

/// Checks that each source fails with an error that contains its message.
fn check_errors(cases: &[(&str, &str)]) {
    for (source, message) in cases {
        let error = run(b"", source).unwrap_err().to_string();
        assert!(error.contains(message), "{source}: {error}");
    }
}

#[test]
fn modules_read_the_grid_and_the_permutation() {
    check(&[
        (
            "return p.HorBlock .. p.VerBlock .. p.Multiply .. #p.Matrix .. p.Matrix[0] .. p.Matrix[3]",
            "221403",
        ),
        ("return tostring(p.Matrix[4])", "nil"),
        ("return tostring(p.Matrix[-1])", "nil"),
        ("return tostring(#imagepuzzle.New(3, 2).Matrix)", "6"),
        ("return tostring(p.Nope)", "nil"),
    ]);
}

#[test]
fn modules_change_the_permutation_and_the_tile_size_multiple() {
    check(&[
        (
            "p.Matrix[0] = 3; p.Matrix[3] = 0; return p.Matrix[0] .. p.Matrix[3]",
            "30",
        ),
        // An index is truncated toward zero.
        ("p.Matrix[1.9] = 2; return p.Matrix[1] .. p.Matrix[2]", "22"),
        (
            "local m = p.Matrix; m[0] = 2; return tostring(p.Matrix[0])",
            "2",
        ),
        ("p.Multiply = 2; return tostring(p.Multiply)", "2"),
        ("p.Multiply = '4'; return tostring(p.Multiply)", "4"),
        ("p.Nope = 1; return tostring(p.Nope)", "nil"),
        // Upstream ignores a write to a field that only reads.
        ("p.HorBlock = 5; return tostring(p.HorBlock)", "2"),
    ]);
}

#[test]
fn bad_values_from_modules_are_errors() {
    check_errors(&[
        ("p.Matrix[4] = 1", "Matrix[4] is outside a 2x2 grid"),
        ("p.Matrix[-1] = 1", "Matrix[-1] is outside a 2x2 grid"),
        (
            "p.Matrix[0] = 'x'",
            "bad argument #3 (number expected, got string)",
        ),
        (
            "return p.Matrix.x",
            "bad argument #2 (number expected, got string)",
        ),
        (
            "p.Multiply = {}",
            "bad argument #3 (number expected, got table)",
        ),
        (
            "imagepuzzle.Create('x', 2)",
            "bad argument #1 (number expected, got string)",
        ),
        (
            "imagepuzzle.Create(2^40, 2^40)",
            "fmd.imagepuzzle.Create: a",
        ),
        ("p.DeScramble(DOC)", "expects two streams"),
        ("p.DeScramble(p, p)", "expects two streams"),
        (
            "p.DeScramble(DOC, DOC)",
            "fmd.imagepuzzle.DeScramble: decode",
        ),
    ]);
}

#[test]
fn descramble_rewrites_the_document_in_place() {
    let destinations = [4, 0, 8, 2, 6, 1, 7, 3, 5];
    let original = grid_image(3, 3, 12, 12);
    let scrambled = png(&scramble(&original, 3, 12, 12, &destinations));
    let source = "
        local q = imagepuzzle.Create(3, 3)
        local destinations = {4, 0, 8, 2, 6, 1, 7, 3, 5}
        for i = 0, 8 do q.Matrix[i] = destinations[i + 1] end
        q.DeScramble(DOC, DOC)
        return tostring(q.Matrix[2])";
    let (result, document) = run(&scrambled, source).unwrap();
    assert_eq!(result, "8");
    assert_same_pixels(&decode(&document).1, &original, "HTTP.Document");
}
