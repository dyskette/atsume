/// Why an expression could not be evaluated.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
pub enum Error {
    /// The expression is not valid.
    #[error("cannot compile {expression:?}: {message}")]
    Compile {
        /// The expression as given.
        expression: String,
        /// What the engine reported.
        message: String,
    },
    /// The expression is valid but failed while running, for instance on a
    /// type error.
    #[error("evaluating {expression:?} failed: {message}")]
    Evaluate {
        /// The expression as given.
        expression: String,
        /// What the engine reported.
        message: String,
    },
}

impl Error {
    /// Names `expression` as the one that failed, in place of the form it
    /// was rewritten to.
    pub(crate) fn for_expression(self, expression: &str) -> Self {
        let expression = expression.to_owned();
        match self {
            Self::Compile { message, .. } => Self::Compile {
                expression,
                message,
            },
            Self::Evaluate { message, .. } => Self::Evaluate {
                expression,
                message,
            },
        }
    }
}
