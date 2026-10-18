//! `fmd.imagepuzzle`, which reassembles images that a site serves cut into
//! tiles and shuffled.
//!
//! A module builds a puzzle over the site's grid, copies the site's
//! permutation into `Matrix`, and calls `DeScramble(HTTP.Document,
//! HTTP.Document)` to rewrite the response in place. `docs/MODULES.md`
//! describes the output formats.

mod descramble;
#[cfg(test)]
mod tests;

use std::cell::{Ref, RefCell, RefMut};
use std::rc::Rc;

use mlua::{Lua, MetaMethod, Table, UserData, UserDataFields, UserDataMethods, Value};

use crate::http::HttpSession;
use crate::runtime::bind::ignore_unknown_fields;
use crate::runtime::convert::int_arg;
use crate::upstream::document::DocumentObject;

use descramble::TilePuzzle;

/// Builds the `fmd.imagepuzzle` table.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let library = lua.create_table()?;
    let create = lua.create_function(|lua, (columns, rows): (Value, Value)| {
        let columns = int_arg(lua, &columns, 1)?;
        let rows = int_arg(lua, &rows, 2)?;
        let puzzle = TilePuzzle::new(columns, rows)
            .map_err(|error| mlua::Error::runtime(format!("fmd.imagepuzzle.Create: {error}")))?;
        Ok(PuzzleObject::new(puzzle))
    })?;
    // Modules call it by either name.
    library.set("Create", create.clone())?;
    library.set("New", create)?;
    Ok(library)
}

/// The object that `Create` returns, over a [`TilePuzzle`] that it shares
/// with the `Matrix` objects it hands out.
#[derive(Clone)]
struct PuzzleObject {
    puzzle: Rc<RefCell<TilePuzzle>>,
}

impl PuzzleObject {
    fn new(puzzle: TilePuzzle) -> Self {
        Self {
            puzzle: Rc::new(RefCell::new(puzzle)),
        }
    }

    fn read(&self) -> mlua::Result<Ref<'_, TilePuzzle>> {
        self.puzzle
            .try_borrow()
            .map_err(|_| mlua::Error::runtime("the image puzzle is being changed"))
    }

    fn write(&self) -> mlua::Result<RefMut<'_, TilePuzzle>> {
        self.puzzle
            .try_borrow_mut()
            .map_err(|_| mlua::Error::runtime("the image puzzle is already in use"))
    }

    /// Reassembles the image in `input` and stores the result in `output`
    /// (`DeScramble`).
    ///
    /// Modules usually pass `HTTP.Document` as both, so the input is read in
    /// full before the output is written.
    fn descramble_documents(&self, lua: &Lua, input: &Value, output: &Value) -> mlua::Result<()> {
        let (Some(input), Some(output)) = (
            DocumentObject::<HttpSession>::from_lua_value(input),
            DocumentObject::<HttpSession>::from_lua_value(output),
        ) else {
            return Err(mlua::Error::runtime(
                "fmd.imagepuzzle.DeScramble: expects two streams",
            ));
        };
        let scrambled = input.bytes(lua)?;
        let descrambled = self.read()?.descramble(&scrambled).map_err(|error| {
            mlua::Error::runtime(format!("fmd.imagepuzzle.DeScramble: {error}"))
        })?;
        output.replace(lua, descrambled)
    }
}

impl UserData for PuzzleObject {
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        fields.add_field_method_get("HorBlock", |_, object| Ok(object.read()?.columns));
        fields.add_field_method_get("VerBlock", |_, object| Ok(object.read()?.rows));
        fields.add_field_method_get("Multiply", |_, object| {
            Ok(object.read()?.tile_size_multiple)
        });
        fields.add_field_method_set("Multiply", |lua, object, value: Value| {
            object.write()?.tile_size_multiple = int_arg(lua, &value, 3)?;
            Ok(())
        });
        fields.add_field_method_get("Matrix", |_, object| {
            Ok(MatrixObject {
                puzzle: object.clone(),
            })
        });
        fields.add_field_method_get("DeScramble", |lua, object| {
            let object = object.clone();
            lua.create_function(move |lua, (input, output): (Value, Value)| {
                object.descramble_documents(lua, &input, &output)
            })
        });
    }

    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        ignore_unknown_fields(methods);
    }
}

/// The puzzle's `Matrix`, which maps each scrambled tile's number to the
/// number of the place it belongs in.
///
/// Indexes start at 0, as upstream's do. Reading outside the grid gives
/// `nil`, and writing outside it is an error.
struct MatrixObject {
    puzzle: PuzzleObject,
}

impl MatrixObject {
    fn get(&self, lua: &Lua, index: &Value) -> mlua::Result<Option<i64>> {
        let index = int_arg(lua, index, 2)?;
        let puzzle = self.puzzle.read()?;
        Ok(tile_index(index).and_then(|index| puzzle.destinations.get(index).copied()))
    }

    fn set(&self, lua: &Lua, index: &Value, destination: &Value) -> mlua::Result<()> {
        let index = int_arg(lua, index, 2)?;
        let mut puzzle = self.puzzle.write()?;
        let (columns, rows) = (puzzle.columns, puzzle.rows);
        let Some(slot) = tile_index(index).and_then(|index| puzzle.destinations.get_mut(index))
        else {
            return Err(mlua::Error::runtime(format!(
                "fmd.imagepuzzle: Matrix[{index}] is outside a {columns}x{rows} grid"
            )));
        };
        *slot = int_arg(lua, destination, 3)?;
        Ok(())
    }

    fn len(&self) -> mlua::Result<usize> {
        Ok(self.puzzle.read()?.destinations.len())
    }
}

impl UserData for MatrixObject {
    fn add_methods<M: UserDataMethods<Self>>(methods: &mut M) {
        methods.add_meta_method(MetaMethod::Index, |lua, matrix, index: Value| {
            matrix.get(lua, &index)
        });
        methods.add_meta_method(
            MetaMethod::NewIndex,
            |lua, matrix, (index, destination): (Value, Value)| {
                matrix.set(lua, &index, &destination)
            },
        );
        methods.add_meta_method(MetaMethod::Len, |_, matrix, ()| matrix.len());
    }
}

/// Returns `index` as a position in the permutation, or `None` when it is
/// negative.
fn tile_index(index: i64) -> Option<usize> {
    usize::try_from(index).ok()
}
