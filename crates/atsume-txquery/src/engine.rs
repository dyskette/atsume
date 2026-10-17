//! Compiling expressions, once each.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use xee_xpath::Queries;
use xee_xpath::query::SequenceQuery;

use crate::error::Error;

/// How many compiled expressions an engine keeps. Modules use a few dozen
/// expressions each; the bound only stops expressions built from page content
/// from growing the cache without end.
const CACHE_LIMIT: usize = 1024;

/// Compiles expressions and keeps them for reuse.
///
/// Modules pass expressions as strings, often the same one inside a loop, so
/// each is compiled once. One engine serves every document of a scrape.
#[derive(Default)]
pub struct Engine {
    queries: Queries<'static>,
    compiled: RefCell<HashMap<String, Rc<SequenceQuery>>>,
}

impl Engine {
    /// Returns an engine with nothing compiled yet.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Returns the compiled form of `expression`.
    pub(crate) fn compile(&self, expression: &str) -> Result<Rc<SequenceQuery>, Error> {
        if let Some(query) = self.compiled.borrow().get(expression) {
            return Ok(Rc::clone(query));
        }
        let query = self
            .queries
            .sequence(expression)
            .map_err(|e| Error::Compile {
                expression: expression.to_owned(),
                message: e.to_string(),
            })?;
        let query = Rc::new(query);
        let mut compiled = self.compiled.borrow_mut();
        if compiled.len() >= CACHE_LIMIT {
            compiled.clear();
        }
        compiled.insert(expression.to_owned(), Rc::clone(&query));
        Ok(query)
    }

    #[cfg(test)]
    pub(crate) fn compiled_count(&self) -> usize {
        self.compiled.borrow().len()
    }
}
