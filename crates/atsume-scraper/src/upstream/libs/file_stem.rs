//! `fmd.fileutil`, which reads the parts of a path for modules.

use mlua::{Lua, Table};

use super::bytes_function;

/// Builds the `fmd.fileutil` table.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let fileutil = lua.create_table()?;
    fileutil.set("ExtractFileNameOnly", bytes_function(lua, file_stem)?)?;
    Ok(fileutil)
}

/// Returns the last part of a path without its extension
/// (`ExtractFileNameOnly`).
///
/// Both `/` and `\` separate the parts, because upstream runs on Windows. A name
/// that only starts with a dot, such as `.hidden`, has no extension.
fn file_stem(path: &[u8]) -> Vec<u8> {
    let name_start = path
        .iter()
        .rposition(|&byte| matches!(byte, b'/' | b'\\'))
        .map_or(0, |separator| separator + 1);
    let name = &path[name_start..];
    let stem = match name.iter().rposition(|&byte| byte == b'.') {
        Some(dot) if dot > 0 => &name[..dot],
        _ => name,
    };
    stem.to_vec()
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::testing::eval_registered;

    #[test]
    fn the_stem_is_the_last_part_without_its_extension() {
        assert_eq!(file_stem(b"/pages/001.jpg"), b"001");
        assert_eq!(file_stem(br"C:\pages\cover.min.png"), b"cover.min");
        assert_eq!(file_stem(b"mixed/sep\\name.txt"), b"name");
        assert_eq!(file_stem(b"no-extension"), b"no-extension");
        assert_eq!(file_stem(b"dir.d/.hidden"), b".hidden");
        assert_eq!(file_stem(b"ends/"), b"");
    }

    #[test]
    fn modules_reach_it_through_the_library() {
        let source = "return fileutil.ExtractFileNameOnly('/tmp/page_12.webp')";
        let stem = eval_registered(|lua| lua.globals().set("fileutil", build(lua)?), source);
        assert_eq!(stem.unwrap(), "page_12");
    }
}
