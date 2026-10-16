//! The HTTP session behind one scrape, independent of Lua.

use std::sync::Arc;
use std::time::Duration;

use url::Url;

use super::client::{Client, Exchange, SendError};
use super::flaresolverr::{Flaresolverr, has_challenge_markers, looks_like_challenge};
use super::transport::{Method, Request};
use super::{DEFAULT_USER_AGENT, HttpConfig, LastRequest};
use crate::cancel::{CancelToken, Cancelled};
use crate::strings::Strings;

/// The requests of one scrape, and what the module set up for them.
///
/// Cookies and headers persist across the handler calls of a scrape, as
/// modules expect. A request reports success as a boolean, which is what
/// handlers test, and records the outcome for the module to read.
pub(crate) struct HttpSession {
    client: Client,
    solver: Option<Arc<Flaresolverr>>,
    backoff: Duration,
    /// Request headers the module set, as `name=value`.
    pub(crate) headers: Strings,
    /// Cookies the module set or a server issued, as `name=value`.
    pub(crate) cookies: Strings,
    /// The body of the last response.
    pub(crate) document: Vec<u8>,
    pub(crate) mime_type: String,
    pub(crate) user_agent: String,
    pub(crate) retry_count: i64,
    /// Set once a request was refused because the scrape was cancelled.
    pub(crate) terminated: bool,
    pub(crate) last: LastRequest,
}

impl HttpSession {
    pub(crate) fn new(config: &HttpConfig, cancel: CancelToken) -> Self {
        let client = Client::new(
            Arc::clone(&config.transport),
            Arc::clone(&config.limiter),
            cancel,
        );
        Self {
            client,
            solver: config.solver.clone(),
            backoff: config.backoff,
            headers: Strings::default(),
            cookies: Strings::default(),
            document: Vec::new(),
            mime_type: String::new(),
            user_agent: DEFAULT_USER_AGENT.to_owned(),
            retry_count: 2,
            terminated: false,
            last: LastRequest::default(),
        }
    }

    /// Clears what one request set up, keeping cookies.
    pub(crate) fn reset(&mut self) {
        self.headers.clear();
        self.mime_type.clear();
        self.document.clear();
        self.last.status = 0;
    }

    /// Forgets every cookie, the module's own and those in the jar.
    pub(crate) fn clear_cookies(&mut self) {
        self.cookies.clear();
        self.client.clear_cookies();
    }

    /// Sends a request unless the scrape is cancelled, and reports whether it
    /// succeeded.
    ///
    /// A cancelled scrape marks the session terminated, so a module looping
    /// over requests ends at the next one instead of carrying on.
    pub(crate) fn send(
        &mut self,
        method: Method,
        url: &str,
        body: &str,
    ) -> Result<bool, Cancelled> {
        let sent = self
            .client
            .cancel_token()
            .check()
            .and_then(|()| self.request(method, url, body));
        self.terminated |= sent.is_err();
        sent
    }

    /// Requests `url`, retrying a failure that a retry might fix.
    fn request(&mut self, method: Method, url: &str, body: &str) -> Result<bool, Cancelled> {
        self.document.clear();
        self.last.status = 0;
        self.last.error = None;
        url.clone_into(&mut self.last.requested);

        for attempt in 0..=self.retry_count {
            if attempt > 0 {
                let pause = self.backoff * u32::try_from(attempt).unwrap_or(u32::MAX);
                self.client.cancel_token().sleep(pause)?;
            }
            match self.attempt(method, url, body) {
                Ok(true) => return Ok(true),
                Ok(false) => {}
                Err(SendError::Cancelled) => return Err(Cancelled),
                Err(SendError::Failed(why)) => self.last.error = Some(why),
            }
            // An interstitial can be solved where an ordinary refusal cannot,
            // so it is tried before giving up on a 4xx.
            if self.try_solve(url)? {
                return Ok(true);
            }
            // A 4xx is a real answer, which a retry will not change.
            if (400..500).contains(&self.last.status) {
                return Ok(false);
            }
        }
        Ok(false)
    }

    /// Makes one request and records its outcome. Returns whether the status
    /// was a success, or why no response arrived.
    fn attempt(&mut self, method: Method, url: &str, body: &str) -> Result<bool, SendError> {
        let parsed = Url::parse(url).map_err(|e| SendError::Failed(format!("{url}: {e}")))?;
        let request = Request {
            method,
            url: parsed.clone(),
            headers: self.request_headers(body),
            body: body.as_bytes().to_vec(),
        };
        let Exchange {
            url: final_url,
            response,
        } = self.client.fetch(request, self.cookies.items())?;

        self.last.status = i64::from(response.status);
        self.last.url = final_url.to_string();
        if self.last.moved.is_none() && final_url.host_str() != parsed.host_str() {
            let from = parsed.host_str().unwrap_or_default().to_owned();
            self.last.moved = Some((from, final_url.origin().ascii_serialization()));
        }
        // Modules read the cookies after a login, to see whether the server
        // issued the session cookie they need.
        for header in response.headers_named("Set-Cookie") {
            if let Ok(cookie) = cookie::Cookie::parse(header) {
                self.cookies.set_value(cookie.name(), cookie.value());
            }
        }
        self.last.challenged |= has_challenge_markers(&response.body);
        self.document = response.body;
        Ok((200..400).contains(&response.status))
    }

    fn request_headers(&self, body: &str) -> Vec<(String, String)> {
        let mut headers = vec![("User-Agent".to_owned(), self.user_agent.clone())];
        if !self.mime_type.is_empty() && !body.is_empty() {
            headers.push(("Content-Type".to_owned(), self.mime_type.clone()));
        }
        for (name, value) in self.headers.pairs() {
            // A module's header replaces a default of the same name.
            headers.retain(|(existing, _)| !existing.eq_ignore_ascii_case(name));
            headers.push((name.to_owned(), value.to_owned()));
        }
        headers
    }

    /// Tries to recover a refused response through FlareSolverr, and reports
    /// whether the solved page is a success.
    fn try_solve(&mut self, url: &str) -> Result<bool, Cancelled> {
        let Some(solver) = self.solver.clone() else {
            return Ok(false);
        };
        if !looks_like_challenge(self.last.status, &self.document) {
            return Ok(false);
        }
        tracing::info!(url, "anti-bot challenge; solving");
        let solution = match solver.solve(url, self.client.cancel_token()) {
            Ok(solution) => solution,
            Err(SendError::Cancelled) => return Err(Cancelled),
            Err(SendError::Failed(why)) => {
                tracing::warn!(url, error = why, "flaresolverr failed");
                return Ok(false);
            }
        };
        self.document = solution.body;
        self.last.status = i64::from(solution.status);
        // The clearance cookies are bound to the agent that earned them, so
        // both carry forward, or the next request is challenged again.
        if !solution.user_agent.is_empty() {
            self.user_agent = solution.user_agent;
        }
        for (name, value) in &solution.cookies {
            self.cookies.set_value(name, value);
        }
        Ok((200..400).contains(&solution.status))
    }
}
