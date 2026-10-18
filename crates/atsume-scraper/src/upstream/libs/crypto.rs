//! `fmd.crypto`, which hashes and encrypts strings for modules. The table also
//! holds the encoders in [`super::encoding`], because FMD2 puts them there.
//!
//! Every function works on the bytes of a Lua string. A hash, a decrypted body
//! or a page in a legacy encoding therefore reaches the module unchanged. A
//! number passed in place of a string is read as its text.

use aes::{Aes128, Aes192, Aes256};
use ctr::Ctr128BE;
use ctr::cipher::{KeyIvInit, StreamCipher};
use hmac::{Hmac, KeyInit, Mac};
use mlua::{Function, Lua, Table, Value};
use sha2::{Digest, Sha256};

use super::encoding::{self, hex};
use super::{BytesTransform, bytes_function};
use crate::runtime::convert::bytes_arg;

/// The hashes of one string, each under the name modules call it by.
const HASH_FUNCTIONS: [(&str, BytesTransform); 2] = [("SHA256", sha256), ("SHA256Hex", sha256_hex)];

/// Builds the `fmd.crypto` table, with the encoders, the hashes and the cipher.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let crypto = lua.create_table()?;
    for (name, transform) in encoding::FUNCTIONS.into_iter().chain(HASH_FUNCTIONS) {
        crypto.set(name, bytes_function(lua, transform)?)?;
    }
    crypto.set("HMAC_SHA256", hmac_function(lua, <[u8]>::to_vec)?)?;
    crypto.set("HMAC_SHA256Hex", hmac_function(lua, hex::encode)?)?;
    crypto.set("AESCTR", aes_ctr_function(lua)?)?;
    Ok(crypto)
}

/// Returns the 32 bytes of the SHA-256 digest of `bytes` (`SHA256`).
fn sha256(bytes: &[u8]) -> Vec<u8> {
    Sha256::digest(bytes).to_vec()
}

/// Returns the SHA-256 digest of `bytes` as lowercase hexadecimal
/// (`SHA256Hex`).
fn sha256_hex(bytes: &[u8]) -> Vec<u8> {
    hex::encode(&sha256(bytes))
}

/// Builds a function `(data, key)` that returns the HMAC-SHA256 of `data`
/// under `key`, in the form that `encode` writes. `build` registers it twice,
/// raw (`HMAC_SHA256`) and as hexadecimal (`HMAC_SHA256Hex`).
fn hmac_function(lua: &Lua, encode: BytesTransform) -> mlua::Result<Function> {
    lua.create_function(move |lua, (data, key): (Value, Value)| {
        let data = bytes_arg(lua, &data, 1)?;
        let key = bytes_arg(lua, &key, 2)?;
        let mut mac = Hmac::<Sha256>::new_from_slice(&key).map_err(mlua::Error::external)?;
        mac.update(&data);
        lua.create_string(encode(&mac.finalize().into_bytes()))
    })
}

/// Builds the function `AESCTR(data, key, iv)`, which encrypts or decrypts
/// `data` with AES in counter mode.
///
/// The key's length picks AES-128, AES-192 or AES-256. The whole 16-byte `iv`
/// is the counter, counted up as one big-endian number. A key or `iv` of
/// another length is an error.
fn aes_ctr_function(lua: &Lua) -> mlua::Result<Function> {
    lua.create_function(|lua, (data, key, iv): (Value, Value, Value)| {
        let data = bytes_arg(lua, &data, 1)?;
        let key = bytes_arg(lua, &key, 2)?;
        let iv = bytes_arg(lua, &iv, 3)?;
        let output = aes_ctr(&data, &key, &iv)
            .map_err(|why| mlua::Error::runtime(format!("fmd.crypto.AESCTR: {why}")))?;
        lua.create_string(output)
    })
}

/// Applies AES in counter mode to `data`, with the AES variant that the key's
/// length calls for.
fn aes_ctr(data: &[u8], key: &[u8], iv: &[u8]) -> Result<Vec<u8>, String> {
    match key.len() {
        16 => apply_keystream::<Ctr128BE<Aes128>>(data, key, iv),
        24 => apply_keystream::<Ctr128BE<Aes192>>(data, key, iv),
        32 => apply_keystream::<Ctr128BE<Aes256>>(data, key, iv),
        other => Err(format!("the key is {other} bytes, want 16, 24 or 32")),
    }
}

/// XORs `data` with the keystream of the cipher `C`. The same operation
/// encrypts plain text and decrypts cipher text.
fn apply_keystream<C: KeyIvInit + StreamCipher>(
    data: &[u8],
    key: &[u8],
    iv: &[u8],
) -> Result<Vec<u8>, String> {
    let mut cipher = C::new_from_slices(key, iv)
        .map_err(|_| format!("the iv is {} bytes, want 16", iv.len()))?;
    let mut output = data.to_vec();
    cipher
        .try_apply_keystream(&mut output)
        .map_err(|error| error.to_string())?;
    Ok(output)
}

#[cfg(test)]
mod tests;
