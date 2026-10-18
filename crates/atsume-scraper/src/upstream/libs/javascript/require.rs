//! The `require` function that page scripts call, the only way a script can
//! reach anything outside its own runtime.
//!
//! It loads `.js` helpers from the checkout's `lua` directory as CommonJS
//! modules, and it serves `crypto`, a source of random numbers.

use std::path::{Path, PathBuf};

use rquickjs::{Coerced, Ctx, Exception, Function, Object, Value};

use super::script_runner::sloppy_eval_options;
use crate::runtime::sandbox::resolve_in_root;

/// The most bytes that one `getRandomValues` call fills, the quota that the
/// Web Crypto standard sets. The filling loop runs in Rust, where a script's
/// time limit cannot stop it, so the quota also bounds that loop.
const MAX_RANDOM_VALUE_BYTES: u32 = 1 << 16;

/// The bytes that each element counts for in an array without a
/// `byteLength`, such as a plain object with a `length`. Each element receives
/// a 32-bit number.
const BYTES_PER_UNTYPED_ELEMENT: i64 = 4;

/// The most bytes that one `randomBytes` call returns.
const MAX_RANDOM_BYTES: u32 = 1 << 20;

/// The JavaScript that builds `require` from two functions that Rust defines.
///
/// `require` is written in JavaScript so that the helpers it has run stay in
/// a JavaScript closure. QuickJS frees a runtime only once nothing holds its
/// objects, and it cannot see an object that a Rust closure holds, so a cache
/// kept in Rust would keep the whole runtime alive.
///
/// A helper runs once per runtime as a CommonJS module, with `this` as the
/// global object. The helpers are UMD bundles, which detect CommonJS by the
/// `exports`, `module` and `require` they receive and assign their API to
/// `module.exports`.
const REQUIRE_SOURCE: &str = "(function (readHelper, cryptoModule) {
    var makeFunction = Function;
    var loaded = Object.create(null);
    return function require(name) {
        name = String(name);
        if (name === 'crypto') return cryptoModule();
        var helper = readHelper(name);
        if (!(helper.path in loaded)) {
            var module = { exports: {} };
            makeFunction('exports', 'module', 'require', helper.source)
                .call(undefined, module.exports, module, require);
            loaded[helper.path] = module.exports;
        }
        return loaded[helper.path];
    };
})";

/// Defines the global `require`, which reads helpers from `lua_dir`.
///
/// `crypto` is built in. Any other name is a file under `lua_dir`, given
/// either as a path ending in `.js` or as a dotted name, which reads its dots
/// as directory separators. A second `require` of the same file returns what
/// the first one returned.
pub(super) fn install<'js>(ctx: &Ctx<'js>, lua_dir: PathBuf) -> rquickjs::Result<()> {
    let read_helper = Function::new(ctx.clone(), move |ctx, name: Coerced<String>| {
        read_helper(&ctx, &lua_dir, &name.0)
    })?;
    let crypto = Function::new(ctx.clone(), |ctx: Ctx<'js>| crypto_module(&ctx))?;
    let make_require: Function = ctx.eval_with_options(REQUIRE_SOURCE, sloppy_eval_options())?;
    let require: Function = make_require.call((read_helper, crypto))?;
    ctx.globals().set("require", require)
}

/// Returns the helper `name` as an object with two properties: `path`, its
/// canonical path, and `source`, its text.
///
/// # Errors
///
/// Throws when the helper lies outside `lua_dir`, or cannot be read.
fn read_helper<'js>(ctx: &Ctx<'js>, lua_dir: &Path, name: &str) -> rquickjs::Result<Object<'js>> {
    let failure =
        |message: &str| Exception::throw_message(ctx, &format!("require({name:?}): {message}"));
    let path = resolve_in_root(lua_dir, &helper_path(name)).map_err(|message| failure(&message))?;
    let source = std::fs::read_to_string(&path).map_err(|error| failure(&error.to_string()))?;
    let helper = Object::new(ctx.clone())?;
    helper.set("path", path.to_string_lossy().into_owned())?;
    helper.set("source", source)?;
    Ok(helper)
}

/// Returns the path, relative to the `lua` directory, that a `require` name
/// refers to. Backslashes read as slashes, because modules are written for
/// Windows.
fn helper_path(name: &str) -> String {
    let path = name.replace('\\', "/");
    let has_js_extension = Path::new(&path)
        .extension()
        .is_some_and(|extension| extension.eq_ignore_ascii_case("js"));
    if has_js_extension {
        return path;
    }
    format!("{}.js", path.replace('.', "/"))
}

/// Builds the `crypto` module, the randomness that crypto-js looks for.
///
/// crypto-js asks `window.crypto`, then `global.crypto`, then
/// `require("crypto")`, and throws if none answers. Defining a `global` object
/// instead would mislead scripts that test `typeof global` to detect Node.
fn crypto_module<'js>(ctx: &Ctx<'js>) -> rquickjs::Result<Object<'js>> {
    let random_values_function = Function::new(ctx.clone(), |ctx: Ctx<'js>, array: Value<'js>| {
        fill_with_random_values(&ctx, array)
    })?;
    let random_bytes_function =
        Function::new(ctx.clone(), |ctx: Ctx<'js>, length: Coerced<i64>| {
            random_bytes(&ctx, length.0)
        })?;
    let crypto = Object::new(ctx.clone())?;
    crypto.set("getRandomValues", random_values_function)?;
    crypto.set("randomBytes", random_bytes_function)?;
    Ok(crypto)
}

/// Fills each index of `array` with a random 32-bit number and returns
/// `array` (`crypto.getRandomValues`). crypto-js passes a `Uint32Array`.
///
/// A typed array is measured by its `byteLength`, as a browser measures it,
/// so a `Uint32Array` takes at most 16,384 elements. Every element takes at
/// least one byte, so no array takes more elements than the quota has bytes,
/// even one that claims a smaller `byteLength`.
fn fill_with_random_values<'js>(ctx: &Ctx<'js>, array: Value<'js>) -> rquickjs::Result<Value<'js>> {
    let Some(object) = array.as_object() else {
        return Err(Exception::throw_type(
            ctx,
            "getRandomValues expects a typed array",
        ));
    };
    // A length that is missing or negative fills nothing, as it would in a
    // browser.
    let length = object.get::<_, Coerced<i64>>("length")?.0.max(0);
    let byte_length = requested_byte_length(object, length)?;
    let quota = i64::from(MAX_RANDOM_VALUE_BYTES);
    let Some(length) =
        within_limit(length, MAX_RANDOM_VALUE_BYTES).filter(|_| byte_length <= quota)
    else {
        let message = format!(
            "getRandomValues fills at most {MAX_RANDOM_VALUE_BYTES} bytes, and the array holds {byte_length}"
        );
        return Err(Exception::throw_range(ctx, &message));
    };
    for index in 0..length {
        object.set(index, random_u32(ctx)?)?;
    }
    Ok(array)
}

/// Returns how many bytes `array`, which has `length` elements, holds. An
/// array without a `byteLength` counts [`BYTES_PER_UNTYPED_ELEMENT`] bytes for
/// each element.
fn requested_byte_length(array: &Object<'_>, length: i64) -> rquickjs::Result<i64> {
    let byte_length: Value = array.get("byteLength")?;
    if byte_length.is_undefined() {
        return Ok(length.saturating_mul(BYTES_PER_UNTYPED_ELEMENT));
    }
    Ok(byte_length.get::<Coerced<i64>>()?.0)
}

/// Returns an object holding `length` random bytes by index, with a
/// `readInt32LE` method that reads the first four as a little-endian signed
/// number (`crypto.randomBytes`). crypto-js calls it when `getRandomValues`
/// is missing.
fn random_bytes<'js>(ctx: &Ctx<'js>, length: i64) -> rquickjs::Result<Object<'js>> {
    let Some(length) = within_limit(length, MAX_RANDOM_BYTES) else {
        let message = format!("randomBytes returns 0 to {MAX_RANDOM_BYTES} bytes");
        return Err(Exception::throw_range(ctx, &message));
    };
    let mut bytes = vec![0; length as usize];
    getrandom::fill(&mut bytes).map_err(|error| no_randomness_error(ctx, error))?;

    let object = Object::new(ctx.clone())?;
    object.set("length", length)?;
    for (index, byte) in (0..length).zip(&bytes) {
        object.set(index, *byte)?;
    }
    let first_four = bytes
        .first_chunk::<4>()
        .map_or(0, |four| i32::from_le_bytes(*four));
    object.set(
        "readInt32LE",
        Function::new(ctx.clone(), move || first_four)?,
    )?;
    Ok(object)
}

/// Returns `count` when it lies between 0 and `limit`, and `None` otherwise.
fn within_limit(count: i64, limit: u32) -> Option<u32> {
    u32::try_from(count).ok().filter(|&count| count <= limit)
}

/// Returns a random 32-bit number from the operating system.
fn random_u32(ctx: &Ctx<'_>) -> rquickjs::Result<u32> {
    getrandom::u32().map_err(|error| no_randomness_error(ctx, error))
}

/// Throws the error for an operating system that gave no random numbers. A
/// script that wants randomness usually makes keys, which must not fall back
/// to predictable values.
fn no_randomness_error(ctx: &Ctx<'_>, error: getrandom::Error) -> rquickjs::Error {
    Exception::throw_internal(ctx, &format!("no random numbers are available: {error}"))
}
