//! Hexadecimal text, as `fmd.crypto` writes and reads it.

/// The digits of lowercase hexadecimal.
const DIGITS: &[u8; 16] = b"0123456789abcdef";

/// Writes each byte as two lowercase hexadecimal digits. Modules call it as
/// `StrToHex`.
pub(super) fn encode(bytes: &[u8]) -> Vec<u8> {
    bytes
        .iter()
        .flat_map(|&byte| {
            [
                DIGITS[usize::from(byte >> 4)],
                DIGITS[usize::from(byte & 0xf)],
            ]
        })
        .collect()
}

/// Reads pairs of hexadecimal digits, in either case, as bytes. Modules call
/// it as `HexToStr`.
///
/// Reading stops at the first pair that is not hexadecimal, and a lone digit
/// at the end is ignored. The bytes read up to that point are returned.
pub(super) fn decode(text: &[u8]) -> Vec<u8> {
    let (pairs, _) = text.as_chunks::<2>();
    pairs
        .iter()
        .map_while(|&[high, low]| byte_from_digits(high, low))
        .collect()
}

/// Returns the byte that two hexadecimal digits write, or `None` when either
/// is not a hexadecimal digit.
pub(super) fn byte_from_digits(high: u8, low: u8) -> Option<u8> {
    Some(digit_value(high)? << 4 | digit_value(low)?)
}

/// Returns the value of one hexadecimal digit.
fn digit_value(digit: u8) -> Option<u8> {
    let value = char::from(digit).to_digit(16)?;
    u8::try_from(value).ok()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bytes_round_trip_through_hex() {
        assert_eq!(encode(b"\x00\x7f\xffA"), b"007fff41");
        assert_eq!(decode(b"007FFF41"), b"\x00\x7f\xffA");
    }

    #[test]
    fn decoding_stops_at_the_first_pair_that_is_not_hex() {
        assert_eq!(decode(b"4142zz43"), b"AB");
        assert_eq!(decode(b"414"), b"A");
        assert_eq!(decode(b""), b"");
    }
}
