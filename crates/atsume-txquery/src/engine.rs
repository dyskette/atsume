//! Prepares each expression once, so that it can run many times.

use std::cell::RefCell;
use std::collections::HashMap;
use std::rc::Rc;

use xee_xpath::Queries;
use xee_xpath::context::StaticContextBuilder;
use xee_xpath::query::SequenceQuery;
use xot::xmlname::OwnedName;

use crate::error::Error;
use crate::rewrite::{self, json_variable_name};

/// The most compiled expressions an engine keeps. A module uses a few dozen
/// expressions, so the limit is rarely reached. It exists so that expressions
/// built from page content cannot grow the cache without end.
const CACHE_LIMIT: usize = 1024;

/// The collation that comparisons and string functions use. It ignores ASCII
/// case, as upstream's default collation does, so `contains(., "ongoing")`
/// matches `Ongoing`.
const CASE_INSENSITIVE_COLLATION: &str =
    "http://www.w3.org/2005/xpath-functions/collation/html-ascii-case-insensitive";

/// Prepares expressions to run and keeps them for reuse.
///
/// Modules pass expressions as strings, and they often pass the same one many
/// times inside a loop. The engine therefore rewrites and compiles each
/// expression only once. One engine serves every page of a scrape.
#[derive(Default)]
pub struct Engine {
    queries: Queries<'static>,
    prepared: RefCell<HashMap<String, Rc<PreparedExpression>>>,
}

/// An expression that is ready to run. It holds the compiled standard XPath and
/// the `json()` arguments that must be evaluated before the query runs.
pub(crate) struct PreparedExpression {
    pub(crate) query: SequenceQuery,
    /// The `json()` arguments, in the order of the variables that hold their
    /// trees. [`rewrite::json_variable_name`] gives each variable's name.
    pub(crate) json_arguments: Vec<String>,
}

impl Engine {
    /// Returns an engine with nothing prepared yet.
    #[must_use]
    pub fn new() -> Self {
        Self::default()
    }

    /// Checks that `expression` and every `json()` argument inside it compile,
    /// without needing a page to run on.
    ///
    /// # Errors
    ///
    /// Returns the first compile error. The error names the expression or the
    /// argument that failed to compile.
    pub fn check(&self, expression: &str) -> Result<(), Error> {
        let prepared = self.prepare(expression)?;
        prepared
            .json_arguments
            .iter()
            .try_for_each(|argument| self.check(argument))
    }

    /// Rewrites `expression` into standard XPath and compiles it. A later call
    /// with the same expression returns the earlier result.
    pub(crate) fn prepare(&self, expression: &str) -> Result<Rc<PreparedExpression>, Error> {
        if let Some(prepared) = self.prepared.borrow().get(expression) {
            return Ok(Rc::clone(prepared));
        }
        let compile_error = |message: String| Error::CompileFailed {
            expression: expression.to_owned(),
            message,
        };
        let rewritten = rewrite::to_xpath(expression).map_err(compile_error)?;
        let query = self
            .compile(&rewritten.xpath, rewritten.json_arguments.len())
            .map_err(compile_error)?;
        let prepared = Rc::new(PreparedExpression {
            query,
            json_arguments: rewritten.json_arguments,
        });
        let mut cache = self.prepared.borrow_mut();
        if cache.len() >= CACHE_LIMIT {
            cache.clear();
        }
        cache.insert(expression.to_owned(), Rc::clone(&prepared));
        Ok(prepared)
    }

    /// Compiles standard XPath that may refer to the variables `$json0` up to
    /// `$json{variables - 1}`.
    fn compile(&self, xpath: &str, variables: usize) -> Result<SequenceQuery, String> {
        let collation = CASE_INSENSITIVE_COLLATION
            .try_into()
            .map_err(|e: iri_string::validate::Error| e.to_string())?;
        let mut context = StaticContextBuilder::default();
        context.variable_names((0..variables).map(|i| OwnedName::name(&json_variable_name(i))));
        context.default_collation(collation);
        self.queries
            .sequence_with_context(xpath, context.build())
            .map_err(|e| e.to_string())
    }

    #[cfg(test)]
    pub(crate) fn prepared_count(&self) -> usize {
        self.prepared.borrow().len()
    }
}
