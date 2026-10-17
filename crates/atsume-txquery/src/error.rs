/// Why an expression could not be evaluated.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// The expression is not valid XPath, even after rewriting.
    #[error("cannot compile {expression:?}: {message}")]
    CompileFailed {
        /// The expression as the caller wrote it.
        expression: String,
        /// The engine's description of the problem.
        message: String,
    },
    /// The expression compiled but failed while running, for example with a
    /// type error.
    #[error("evaluating {expression:?} failed: {message}")]
    EvaluateFailed {
        /// The expression as the caller wrote it.
        expression: String,
        /// The engine's description of the problem.
        message: String,
    },
}
