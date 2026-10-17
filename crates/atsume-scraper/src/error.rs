//! The failures that can happen while loading or running a module.

use crate::http::LastRequest;

/// The kind of failure that an [`Error`] reports, so that a caller can decide
/// what to do.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
#[non_exhaustive]
pub enum ErrorKind {
    /// The scrape was cancelled.
    Cancelled,
    /// A call into the module used its whole CPU budget.
    CpuLimit,
    /// A module file could not be loaded, or declared no website.
    Load,
    /// The module failed while handling an event. It raised an error, named a
    /// function that it does not define, or produced nothing usable.
    Module,
    /// The module reported that a request it needed failed. The error's
    /// [`last_request`](Error::last_request) says what the site sent back.
    Network,
    /// The page the module was given holds nothing the module recognizes.
    /// This usually means that the site moved the page.
    NotFound,
    /// The site rejected the credentials.
    LoginRejected,
    /// The caller asked for something the module does not offer, such as a
    /// website, handler or setting that it does not declare, or a login
    /// without credentials.
    Unsupported,
}

/// A failure while loading or running a module.
///
/// The message first names where the failure happened, such as the module
/// file or the handler, and then gives the cause.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
#[error("{message}")]
pub struct Error {
    kind: ErrorKind,
    message: String,
    last_request: Option<Box<LastRequest>>,
}

impl Error {
    pub(crate) fn new(kind: ErrorKind, message: impl Into<String>) -> Self {
        Self {
            kind,
            message: message.into(),
            last_request: None,
        }
    }

    /// Reports a network problem that the module ran into while doing
    /// `action`. The error keeps what the module's last request got back.
    pub(crate) fn network(site_name: &str, action: &str, last: LastRequest) -> Self {
        let from = if last.requested_url.is_empty() {
            String::new()
        } else {
            format!(" from {}", last.requested_url)
        };
        let cause = last
            .error
            .as_ref()
            .map(|c| format!(": {c}"))
            .unwrap_or_default();
        let message = format!(
            "{site_name}: network problem {action} (last status {}{from}){cause}",
            last.status
        );
        Self {
            last_request: Some(Box::new(last)),
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

    /// Prefixes the message with where the failure happened, and classifies
    /// the failure as `kind`. A cancellation or a CPU limit keeps its own kind.
    pub(crate) fn with_context(self, context: impl std::fmt::Display, kind: ErrorKind) -> Self {
        let kind = match self.kind {
            ErrorKind::Cancelled | ErrorKind::CpuLimit => self.kind,
            _ => kind,
        };
        Self {
            kind,
            message: format!("{context}: {}", self.message),
            last_request: self.last_request,
        }
    }

    /// Returns what kind of failure this is.
    #[must_use]
    pub fn kind(&self) -> ErrorKind {
        self.kind
    }

    /// Returns what the module's last request got back. Only a
    /// [`Network`](ErrorKind::Network) error has one.
    #[must_use]
    pub fn last_request(&self) -> Option<&LastRequest> {
        self.last_request.as_deref()
    }
}

impl From<mlua::Error> for Error {
    fn from(error: mlua::Error) -> Self {
        Self::new(ErrorKind::Module, error.to_string())
    }
}

/// A result whose error is [`Error`].
pub type Result<T, E = Error> = std::result::Result<T, E>;
