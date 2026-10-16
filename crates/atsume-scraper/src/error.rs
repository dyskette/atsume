use std::path::PathBuf;

/// What can go wrong loading or running a module.
#[derive(Debug, thiserror::Error)]
pub enum Error {
    /// The module file failed to compile or raised while its top level ran.
    #[error("load {}: {source}", file.display())]
    Load {
        /// The module file.
        file: PathBuf,
        /// What Lua reported.
        #[source]
        source: Box<Error>,
    },
    /// `Init()` raised.
    #[error("Init {}: {source}", file.display())]
    Init {
        /// The module file.
        file: PathBuf,
        /// What Lua reported.
        #[source]
        source: Box<Error>,
    },
    /// `Init()` returned without declaring a website.
    #[error("{}: Init never called NewWebsiteModule", file.display())]
    NoDeclaration {
        /// The module file.
        file: PathBuf,
    },
    /// The file declares websites, but none under the name asked for.
    #[error("{} declares no website named {name:?}", file.display())]
    NoSuchSite {
        /// The module file.
        file: PathBuf,
        /// The name asked for.
        name: String,
    },
    /// The module does not assign a function to this event.
    #[error("module {module} has no {event} handler")]
    NoHandler {
        /// The website's declared name.
        module: String,
        /// The event, such as `OnGetInfo`.
        event: String,
    },
    /// The module names a handler function that does not exist.
    #[error("module {module} declares {event}={function:?} but defines no such function")]
    MissingFunction {
        /// The website's declared name.
        module: String,
        /// The event, such as `OnGetInfo`.
        event: String,
        /// The global the module named.
        function: String,
    },
    /// A handler raised. `call` names the website and the function.
    #[error("{call}: {source}")]
    Handler {
        /// `Module/Function`, so a failure names where to look.
        call: String,
        /// What went wrong inside it.
        #[source]
        source: Box<Error>,
    },
    /// One call used its whole CPU budget.
    #[error("CPU limit of {limit} instructions reached; the module ran too long without returning")]
    CpuLimit {
        /// The budget that was used up.
        limit: u64,
    },
    /// The scrape was cancelled. A call cancelled mid-way always ends in
    /// this, even if the module caught the termination and returned.
    #[error("stopped: cancelled")]
    Cancelled,
    /// An error from the Lua runtime.
    #[error(transparent)]
    Lua(#[from] mlua::Error),
    /// Reading a module file failed.
    #[error("{}: {source}", path.display())]
    Io {
        /// The file.
        path: PathBuf,
        /// Why.
        #[source]
        source: std::io::Error,
    },
}

impl Error {
    /// Reports whether this error, or one it wraps, is a cancellation.
    #[must_use]
    pub fn is_cancelled(&self) -> bool {
        match self {
            Error::Cancelled => true,
            Error::Load { source, .. }
            | Error::Init { source, .. }
            | Error::Handler { source, .. } => source.is_cancelled(),
            _ => false,
        }
    }

    /// Reports whether this error, or one it wraps, is the CPU limit.
    #[must_use]
    pub fn is_cpu_limit(&self) -> bool {
        match self {
            Error::CpuLimit { .. } => true,
            Error::Load { source, .. }
            | Error::Init { source, .. }
            | Error::Handler { source, .. } => source.is_cpu_limit(),
            _ => false,
        }
    }
}

/// A result whose error is [`Error`].
pub type Result<T, E = Error> = std::result::Result<T, E>;
