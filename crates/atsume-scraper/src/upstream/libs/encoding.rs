//! The encoders of `fmd.crypto`, which turn text into Base64, hexadecimal,
//! percent-encoding or HTML references, and back.
//!
//! FMD2 puts these encoders in its crypto library, beside the hashes and the
//! cipher in [`super::crypto`], so modules find them all in one table.

pub(crate) mod hex;
mod html_entities;
mod url_encoding;

use base64::Engine as _;
use base64::alphabet::STANDARD;
use base64::engine::general_purpose::{GeneralPurpose, GeneralPurposeConfig};
use base64::engine::{DecodePaddingMode, general_purpose};

use super::BytesTransform;

/// The encoders, each under the name modules call it by.
pub(super) const FUNCTIONS: [(&str, BytesTransform); 9] = [
    ("EncodeURLElement", url_encoding::encode_query_value),
    ("EncodeURL", url_encoding::encode_path),
    ("DecodeURL", url_encoding::decode_query_value),
    ("HTMLEncode", html_entities::encode),
    ("HTMLDecode", html_entities::decode),
    ("EncodeBase64", encode_base64),
    ("DecodeBase64", decode_base64),
    ("StrToHex", hex::encode),
    ("HexToStr", hex::decode),
];

/// Writes bytes as standard Base64 with padding (`EncodeBase64`).
fn encode_base64(bytes: &[u8]) -> Vec<u8> {
    general_purpose::STANDARD.encode(bytes).into_bytes()
}

/// The Base64 engine for reading what sites send. Some sites pad their Base64
/// and some do not, and some leave stray bits set in the last character.
const LENIENT_BASE64: GeneralPurpose = GeneralPurpose::new(
    &STANDARD,
    GeneralPurposeConfig::new()
        .with_decode_padding_mode(DecodePaddingMode::Indifferent)
        .with_decode_allow_trailing_bits(true),
);

/// Reads standard Base64, padded or not, and skips line breaks
/// (`DecodeBase64`). Text that is not Base64 reads as an empty string.
fn decode_base64(text: &[u8]) -> Vec<u8> {
    let without_line_breaks: Vec<u8> = text
        .iter()
        .copied()
        .filter(|byte| !matches!(byte, b'\r' | b'\n'))
        .collect();
    LENIENT_BASE64
        .decode(without_line_breaks)
        .unwrap_or_default()
}
