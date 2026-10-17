//! Fetching pages for modules, free of Lua.
//!
//! A transport sends one request; a client adds redirects, cookies and rate
//! limiting; the session behind one scrape adds retries and anti-bot
//! solving. Modules reach the session through the `HTTP` object in
//! `upstream::http`.

mod client;
mod flaresolverr;
mod limiter;
mod session;
mod transport;

use std::sync::Arc;
use std::time::Duration;

pub use flaresolverr::Flaresolverr;
pub use limiter::{HostLimiter, Limiter, Unlimited};
pub(crate) use session::HttpSession;
pub use transport::{Method, Request, Response, Transport, TransportError, UreqTransport};

/// The user agent FMD2 sends. Some sites serve different markup to an agent
/// that looks automated.
pub const DEFAULT_USER_AGENT: &str = concat!(
    "Mozilla/5.0 (Windows NT 10.0; Win64; x64) ",
    "AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"
);

/// How long one request may take before it counts as failed.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(60);

/// How a scrape reaches the network.
#[derive(Clone)]
pub struct HttpConfig {
    /// Sends each request.
    pub transport: Arc<dyn Transport>,
    /// Spaces out requests to each site.
    pub limiter: Arc<dyn Limiter>,
    /// Gets past anti-bot interstitials, when configured.
    pub solver: Option<Arc<Flaresolverr>>,
    /// How a request that got no usable answer is retried.
    pub retry: RetryPolicy,
}

/// How a request that got no usable answer is retried.
///
/// A refusal with a 4xx status is final and never retried; a server error or
/// a request that got no answer is.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct RetryPolicy {
    /// How many times a request is retried after its first attempt. A module
    /// may change this for its own requests through `HTTP.RetryCount`.
    pub max_retries: u32,
    /// The pause before the first retry. Each later retry waits one more.
    pub backoff_step: Duration,
}

impl Default for RetryPolicy {
    /// Two retries, the first after two seconds and the second after four.
    fn default() -> Self {
        Self {
            max_retries: 2,
            backoff_step: Duration::from_secs(2),
        }
    }
}

impl RetryPolicy {
    /// A policy that tries each request once.
    pub const NONE: Self = Self {
        max_retries: 0,
        backoff_step: Duration::ZERO,
    };
}

impl Default for HttpConfig {
    fn default() -> Self {
        Self {
            transport: Arc::new(UreqTransport::new(REQUEST_TIMEOUT)),
            limiter: Arc::new(Unlimited),
            solver: None,
            retry: RetryPolicy::default(),
        }
    }
}

impl HttpConfig {
    /// Returns a configuration that refuses every request, for loading the
    /// catalog: declaring a website needs no network access.
    pub(crate) fn offline() -> Self {
        Self {
            transport: Arc::new(Offline),
            limiter: Arc::new(Unlimited),
            solver: None,
            retry: RetryPolicy::NONE,
        }
    }
}

/// A transport that refuses every request.
struct Offline;

impl Transport for Offline {
    fn send(&self, request: &Request) -> Result<Response, TransportError> {
        Err(TransportError(format!(
            "{}: no network access while loading the catalog",
            request.url
        )))
    }
}

/// What the last request a module made got back, for explaining a failure.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LastRequest {
    /// The address the request asked for.
    pub requested_url: String,
    /// Where it ended up after redirects, once the site answered.
    pub final_url: String,
    /// The HTTP status, or 0 when the site did not answer.
    pub status: i64,
    /// Why the site did not answer at all, if it did not.
    pub error: Option<String>,
    /// Whether any response so far carried an anti-bot interstitial.
    pub challenged: bool,
    /// The first redirect to another host. A site that moved usually says so
    /// this way.
    pub moved: Option<Moved>,
}

/// A redirect from one host to another.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Moved {
    /// The host the request asked for.
    pub from_host: String,
    /// The origin the redirects ended at, such as `https://new.example`.
    pub to_origin: String,
}

#[cfg(test)]
mod tests;
