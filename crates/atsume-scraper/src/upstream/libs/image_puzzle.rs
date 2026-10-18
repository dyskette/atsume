//! `fmd.imagepuzzle`, which reassembles images that a site serves cut into
//! tiles and shuffled.
//!
//! A module builds a puzzle over the site's grid, copies the site's
//! permutation into `Matrix`, and calls `DeScramble(HTTP.Document,
//! HTTP.Document)` to rewrite the response in place. `docs/MODULES.md`
//! describes the output formats.

#[cfg(test)]
mod tests;
mod tile_puzzle;

use std::cell::{Ref, RefCell, RefMut};
use std::rc::Rc;

use mlua::{Lua, MetaMethod, Table, UserData, UserDataFields, UserDataMethods, Value};

use crate::http::HttpSession;
use crate::runtime::bind::ignore_unknown_fields;
use crate::runtime::convert::int_arg;
use crate::upstream::document::DocumentObject;

use tile_puzzle::TilePuzzle;

/// Builds the `fmd.imagepuzzle` table.
pub(super) fn build(lua: &Lua) -> mlua::Result<Table> {
    let library = lua.create_table()?;
    let create = lua.create_function(|lua, (columns, rows): (Value, Value)| {
        let columns = int_arg(lua, &columns, 1)?;
        let rows = int_arg(lua, &rows, 2)?;
        let puzzle = TilePuzzle::new(columns, rows)
            .map_err(|error| mlua::Error::runtime(format!("fmd.imagepuzzle.Create: {error}")))?;
        Ok(TilePuzzleObject::new(puzzle))
    })?;
    // Modules call it by either name.
    library.set("Create", create.clone())?;
    library.set("New", create)?;
    Ok(library)
}

/// The object that `Create` returns. It shares its [`TilePuzzle`] with every
/// `Matrix` object it hands out.
#[derive(Clone)]
struct TilePuzzleObject {
    puzzle: Rc<RefCell<TilePuzzle>>,
}

impl TilePuzzleObject {
    /// Wraps `puzzle`, so that this object and the `Matrix` objects it hands
    /// out can share it.
    fn new(puzzle: TilePuzzle) -> Self {
        Self {
            puzzle: Rc::new(RefCell::new(puzzle)),
        }
    }

    /// Borrows the puzzle to read it. Fails with a Lua error, instead of
    /// panicking, if the puzzle is being changed.
    fn borrow_puzzle(&self) -> mlua::Result<Ref<'_, TilePuzzle>> {
        self.puzzle
            .try_borrow()
            .map_err(|_| mlua::Error::runtime("the image puzzle is already in use"))
    }

    /// Borrows the puzzle to change it. Fails with a Lua error, instead of
    /// panicking, if the puzzle is already borrowed.
    fn borrow_puzzle_mut(&self) -> mlua::Result<RefMut<'_, TilePuzzle>> {
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
                "fmd.imagepuzzle.DeScramble: expects two documents, such as HTTP.Document",
            ));
        };
        let scrambled = input.bytes(lua)?;
        let descrambled = self
            .borrow_puzzle()?
            .descramble(&scrambled)
            .map_err(|error| {
                mlua::Error::runtime(format!("fmd.imagepuzzle.DeScramble: {error}"))
            })?;
        output.replace(lua, descrambled)
    }
}

impl UserData for TilePuzzleObject {
    /// Exposes the grid's size (`HorBlock` and `VerBlock`), the tile size step
    /// (`Multiply`), the permutation (`Matrix`) and `DeScramble`. Only
    /// `Multiply` can be written; a write to another field is ignored, as
    /// upstream ignores it.
    fn add_fields<F: UserDataFields<Self>>(fields: &mut F) {
        fields.add_field_method_get("HorBlock", |_, object| Ok(object.borrow_puzzle()?.columns));
        fields.add_field_method_get("VerBlock", |_, object| Ok(object.borrow_puzzle()?.rows));
        fields.add_field_method_get("Multiply", |_, object| {
            Ok(object.borrow_puzzle()?.tile_size_multiple)
        });
        fields.add_field_method_set("Multiply", |lua, object, value: Value| {
            object.borrow_puzzle_mut()?.tile_size_multiple = int_arg(lua, &value, 3)?;
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

    /// Makes unknown fields read as `nil` and ignores writes to them.
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
    puzzle: TilePuzzleObject,
}

impl MatrixObject {
    /// Returns the destination of the tile at `index` (`Matrix[index]`), or
    /// `nil` when `index` is outside the grid. A fractional index is truncated
    /// toward zero.
    fn get(&self, lua: &Lua, index: &Value) -> mlua::Result<Option<i64>> {
        let index = int_arg(lua, index, 2)?;
        let puzzle = self.puzzle.borrow_puzzle()?;
        Ok(permutation_index(index).and_then(|index| puzzle.destinations.get(index).copied()))
    }

    /// Sets the destination of the tile at `index`
    /// (`Matrix[index] = destination`).
    ///
    /// Fails when `index` is outside the grid, or when either value is not a
    /// number. The destination itself is checked only when the image is
    /// descrambled.
    fn set(&self, lua: &Lua, index: &Value, destination: &Value) -> mlua::Result<()> {
        let index = int_arg(lua, index, 2)?;
        let mut puzzle = self.puzzle.borrow_puzzle_mut()?;
        let (columns, rows) = (puzzle.columns, puzzle.rows);
        let Some(slot) =
            permutation_index(index).and_then(|index| puzzle.destinations.get_mut(index))
        else {
            return Err(mlua::Error::runtime(format!(
                "fmd.imagepuzzle.Matrix[{index}]: the index is outside a {columns}x{rows} grid"
            )));
        };
        *slot = int_arg(lua, destination, 3)?;
        Ok(())
    }

    /// Returns how many tiles the grid has (`#Matrix`).
    fn len(&self) -> mlua::Result<usize> {
        Ok(self.puzzle.borrow_puzzle()?.destinations.len())
    }
}

impl UserData for MatrixObject {
    /// Sends indexing, assignment and `#` to [`get`](Self::get),
    /// [`set`](Self::set) and [`len`](Self::len).
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
fn permutation_index(index: i64) -> Option<usize> {
    usize::try_from(index).ok()
}
