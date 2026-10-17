//! Compiling expressions, once each.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use xee_xpath::Queries;
use xee_xpath::context::StaticContextBuilder;
use xee_xpath::query::SequenceQuery;
use xot::xmlname::OwnedName;

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

/// Returns the name of the variable bound to the `index`th `json()` call of
/// an expression.
pub(crate) fn json_variable(index: usize) -> String {
    format!("json{index}")
}

impl Engine {
    /// Returns an engine with nothing compiled yet.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Returns the compiled form of `expression`, which may refer to the
    /// variables `$json0` up to `$json{variables - 1}`.
    pub(crate) fn compile(
        &self,
        expression: &str,
        variables: usize,
    ) -> Result<Rc<SequenceQuery>, Error> {
        if let Some(query) = self.compiled.borrow().get(expression) {
            return Ok(Rc::clone(query));
        }
        let mut context = StaticContextBuilder::default();
        context.variable_names((0..variables).map(|i| OwnedName::name(&json_variable(i))));
        let query = self
            .queries
            .sequence_with_context(expression, context.build())
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
