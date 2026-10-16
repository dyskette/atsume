//! Fetching pages for modules: the `HTTP` object and the session behind it.
//!
//! [`session`] holds the requests of one scrape as plain Rust, and
//! [`binding`] exposes it to modules as `HTTP`.

mod binding;
mod client;
mod flaresolverr;
mod limiter;
mod session;
mod transport;

use std::sync::Arc;
use std::time::Duration;

pub(crate) use binding::{HttpObject, add_server_cookies, clear_cookies};
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
    /// The pause before a retry, multiplied by the attempt number.
    pub backoff: Duration,
}

impl Default for HttpConfig {
    fn default() -> Self {
        Self {
            transport: Arc::new(UreqTransport::new(REQUEST_TIMEOUT)),
            limiter: Arc::new(Unlimited),
            solver: None,
            backoff: Duration::from_secs(2),
        }
    }
}

/// What the last request a module made got back, for explaining a failure.
#[derive(Clone, Debug, Default, PartialEq, Eq)]
pub struct LastRequest {
    /// The address the request asked for.
    pub requested: String,
    /// Where it ended up after redirects, once the site answered.
    pub url: String,
    /// The HTTP status, or 0 when the site did not answer.
    pub status: i64,
    /// Why the site did not answer at all, if it did not.
    pub error: Option<String>,
    /// Whether any response so far carried an anti-bot interstitial.
    pub challenged: bool,
    /// The first redirect to another host: the host asked for, and the
    /// address it ended at. A site that moved usually says so this way.
    pub moved: Option<(String, String)>,
}

#[cfg(test)]
mod tests;
