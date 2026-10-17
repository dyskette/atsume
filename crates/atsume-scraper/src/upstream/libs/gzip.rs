//! `fmd.gzip`, which inflates data that a site compressed inside its response.

use std::io::Read;

use flate2::read::MultiGzDecoder;
use mlua::{Lua, Table, Value};

use crate::runtime::convert::bytes_arg;

/// Builds the `fmd.gzip` table.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let gzip = lua.create_table()?;
    let decompress = lua.create_function(|lua, compressed: Value| {
        let compressed = bytes_arg(lua, &compressed, 1)?;
        let inflated = inflate(&compressed)
            .map_err(|error| mlua::Error::runtime(format!("fmd.gzip.Decompress: {error}")))?;
        lua.create_string(inflated)
    })?;
    gzip.set("Decompress", decompress)?;
    Ok(gzip)
}

/// Inflates gzip data. Modules call it as `Decompress`.
///
/// Several gzip members written one after another inflate to their contents
/// joined together.
///
/// # Errors
///
/// Fails when the data is not gzip or is cut short.
fn inflate(compressed: &[u8]) -> std::io::Result<Vec<u8>> {
    let mut inflated = Vec::new();
    MultiGzDecoder::new(compressed).read_to_end(&mut inflated)?;
    Ok(inflated)
}

#[cfg(test)]
mod tests {
    use std::io::Write;

    use flate2::Compression;
    use flate2::write::GzEncoder;

    use super::*;

    fn compress(data: &[u8]) -> Vec<u8> {
        let mut encoder = GzEncoder::new(Vec::new(), Compression::default());
        encoder.write_all(data).unwrap();
        encoder.finish().unwrap()
    }

    /// Runs `source` with `fmd.gzip` in the global `gzip` and `compressed` in
    /// the global `DATA`.
    fn eval(compressed: &[u8], source: &str) -> mlua::Result<String> {
        let lua = Lua::new();
        lua.globals().set("gzip", build(&lua)?)?;
        lua.globals().set("DATA", lua.create_string(compressed)?)?;
        lua.load(source).eval()
    }

    #[test]
    fn decompress_returns_what_was_compressed() {
        let data = compress(b"{\"pages\":[\"1.jpg\",\"2.jpg\"]}");
        assert_eq!(
            eval(&data, "return gzip.Decompress(DATA)").unwrap(),
            r#"{"pages":["1.jpg","2.jpg"]}"#
        );
    }

    #[test]
    fn members_written_one_after_another_are_joined() {
        let mut data = compress(b"first ");
        data.extend(compress(b"second"));
        assert_eq!(inflate(&data).unwrap(), b"first second");
    }

    #[test]
    fn decompress_keeps_binary_data_intact() {
        let binary: Vec<u8> = (0..=255).collect();
        assert_eq!(inflate(&compress(&binary)).unwrap(), binary);
    }

    #[test]
    fn data_that_is_not_gzip_is_an_error() {
        let err = eval(b"plain text", "return gzip.Decompress(DATA)").unwrap_err();
        assert!(err.to_string().contains("fmd.gzip.Decompress"), "{err}");
        let cut_short = compress(b"some longer text to compress");
        assert!(inflate(&cut_short[..cut_short.len() - 4]).is_err());
        assert!(eval(b"", "return gzip.Decompress()").is_err());
    }
}
