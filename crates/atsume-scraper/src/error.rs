//! What can go wrong loading or running a module.

use crate::http::LastRequest;

/// What kind of failure an [`Error`] is, for a caller to decide what to do.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ErrorKind {
    /// The scrape was cancelled.
    Cancelled,
    /// A call into the module used its whole CPU budget.
    CpuLimit,
    /// A module file could not be loaded, or declared no website.
    Load,
    /// The module failed while handling an event: it raised, named a
    /// function it does not define, or produced nothing usable.
    Module,
    /// The module reported that a request it needed failed. The error's
    /// [`request`](Error::request) says what the site sent back.
    Network,
    /// The page the module was given holds nothing it recognises, which
    /// usually means the site moved it.
    NotFound,
    /// The site rejected the credentials.
    LoginRejected,
    /// The caller asked for something the module does not offer: a website,
    /// handler or setting it does not declare, or a login without
    /// credentials.
    Unsupported,
}

/// A failure loading or running a module.
///
/// The message names where it happened, such as the module file or the
/// handler, followed by the cause.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
#[error("{message}")]
pub struct Error {
    kind: ErrorKind,
    message: String,
    request: Option<Box<LastRequest>>,
}

impl Error {
    pub(crate) fn new(kind: ErrorKind, message: impl Into<String>) -> Self {
        Self {
            kind,
            message: message.into(),
            request: None,
        }
    }

    /// Reports a network problem the module ran into while doing `what`,
    /// with what its last request got back.
    pub(crate) fn network(module: &str, what: &str, last: LastRequest) -> Self {
        let from = if last.requested.is_empty() {
            String::new()
        } else {
            format!(" from {}", last.requested)
        };
        let cause = last
            .error
            .as_ref()
            .map(|c| format!(": {c}"))
            .unwrap_or_default();
        let message = format!(
            "{module}: network problem {what} (last status {}{from}){cause}",
            last.status
        );
        Self {
            request: Some(Box::new(last)),
            ..Self::new(ErrorKind::Network, message)
        }
    }

    pub(crate) fn cancelled() -> Self {
        Self::new(ErrorKind::Cancelled, "stopped: cancelled")
    }

    pub(crate) fn cpu_limit(limit: u64) -> Self {
        let message = format!(
            "CPU limit of {limit} instructions reached; the module ran too long without returning"
        );
        Self::new(ErrorKind::CpuLimit, message)
    }

    /// Prefixes where the failure happened, and classifies it as `kind`
    /// unless it is a cancellation or the CPU limit, which stay what they are.
    pub(crate) fn within(self, context: impl std::fmt::Display, kind: ErrorKind) -> Self {
        let kind = match self.kind {
            ErrorKind::Cancelled | ErrorKind::CpuLimit => self.kind,
            _ => kind,
        };
        Self {
            kind,
            message: format!("{context}: {}", self.message),
            request: self.request,
        }
    }

    /// Returns what kind of failure this is.
    #[must_use]
    pub fn kind(&self) -> ErrorKind {
        self.kind
    }

    /// Returns what the module's last request got back, for a
    /// [`Network`](ErrorKind::Network) error.
    #[must_use]
    pub fn request(&self) -> Option<&LastRequest> {
        self.request.as_deref()
    }
}

impl From<mlua::Error> for Error {
    fn from(error: mlua::Error) -> Self {
        Self::new(ErrorKind::Module, error.to_string())
    }
}

/// A result whose error is [`Error`].
pub type Result<T, E = Error> = std::result::Result<T, E>;
