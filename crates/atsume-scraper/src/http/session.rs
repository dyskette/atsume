//! The HTTP session behind one scrape, independent of Lua.

use std::sync::Arc;
use std::time::Duration;

use url::Url;

use super::client::{Client, Exchange, FetchError};
use super::flaresolverr::{Flaresolverr, has_challenge_markers, looks_like_challenge};
use super::transport::{Method, Request};
use super::{DEFAULT_USER_AGENT, HttpConfig, LastRequest, Moved};
use crate::cancel::{CancelToken, Cancelled};
use crate::string_list::StringList;

/// The requests of one scrape, and what the module set up for them.
///
/// Cookies and headers persist across the handler calls of a scrape, as
/// modules expect. A request reports success as a boolean, which is what
/// handlers test, and records the outcome for the module to read.
pub(crate) struct HttpSession {
    client: Client,
    solver: Option<Arc<Flaresolverr>>,
    backoff_step: Duration,
    /// Request headers the module set, as `name=value`.
    pub(crate) headers: StringList,
    /// Cookies the module set or a server issued, as `name=value`.
    pub(crate) cookies: StringList,
    /// The body of the last response. Modules see it as `HTTP.Document`.
    pub(crate) response_body: Vec<u8>,
    /// The `Content-Type` of a request with a body. Modules see it as
    /// `HTTP.MimeType`.
    pub(crate) request_content_type: String,
    /// Modules see it as `HTTP.UserAgent`.
    pub(crate) user_agent: String,
    /// How many times a failed request is retried. Modules see it as
    /// `HTTP.RetryCount`.
    pub(crate) max_retries: i64,
    /// Set once a request was refused because the scrape was cancelled.
    /// Modules see it as `HTTP.Terminated`.
    pub(crate) cancelled: bool,
    /// What the last request got back, which modules read as `HTTP.LastURL`
    /// and `HTTP.ResultCode`.
    pub(crate) last_request: LastRequest,
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
            backoff_step: config.retry.backoff_step,
            headers: StringList::default(),
            cookies: StringList::default(),
            response_body: Vec::new(),
            request_content_type: String::new(),
            user_agent: DEFAULT_USER_AGENT.to_owned(),
            max_retries: i64::from(config.retry.max_retries),
            cancelled: false,
            last_request: LastRequest::default(),
        }
    }

    /// Clears what one request set up, keeping cookies.
    pub(crate) fn reset(&mut self) {
        self.headers.clear();
        self.request_content_type.clear();
        self.response_body.clear();
        self.last_request.status = 0;
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
            .and_then(|()| self.send_with_retries(method, url, body));
        self.cancelled |= sent.is_err();
        sent
    }

    /// Requests `url`, retrying a failure that a retry might fix.
    fn send_with_retries(
        &mut self,
        method: Method,
        url: &str,
        body: &str,
    ) -> Result<bool, Cancelled> {
        self.response_body.clear();
        self.last_request.status = 0;
        self.last_request.error = None;
        url.clone_into(&mut self.last_request.requested_url);

        for attempt in 0..=self.max_retries {
            if attempt > 0 {
                let pause = self.backoff_step * u32::try_from(attempt).unwrap_or(u32::MAX);
                self.client.cancel_token().sleep(pause)?;
            }
            match self.send_once(method, url, body) {
                Ok(true) => return Ok(true),
                Ok(false) => {}
                Err(FetchError::Cancelled) => return Err(Cancelled),
                Err(FetchError::Failed(why)) => self.last_request.error = Some(why),
            }
            // An interstitial can be solved where an ordinary refusal cannot,
            // so it is tried before giving up on a 4xx.
            if self.try_solve(url)? {
                return Ok(true);
            }
            // A 4xx is a real answer, which a retry will not change.
            if (400..500).contains(&self.last_request.status) {
                return Ok(false);
            }
        }
        Ok(false)
    }

    /// Makes one request and records its outcome. Returns whether the status
    /// was a success, or why no response arrived.
    fn send_once(&mut self, method: Method, url: &str, body: &str) -> Result<bool, FetchError> {
        let parsed = Url::parse(url).map_err(|e| FetchError::Failed(format!("{url}: {e}")))?;
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

        self.last_request.status = i64::from(response.status);
        self.last_request.final_url = final_url.to_string();
        if self.last_request.moved.is_none() && final_url.host_str() != parsed.host_str() {
            self.last_request.moved = Some(Moved {
                from_host: parsed.host_str().unwrap_or_default().to_owned(),
                to_origin: final_url.origin().ascii_serialization(),
            });
        }
        // Modules read the cookies after a login, to see whether the server
        // issued the session cookie they need.
        for header in response.headers_named("Set-Cookie") {
            if let Ok(cookie) = cookie::Cookie::parse(header) {
                self.cookies.set_value(cookie.name(), cookie.value());
            }
        }
        self.last_request.challenged |= has_challenge_markers(&response.body);
        self.response_body = response.body;
        Ok((200..400).contains(&response.status))
    }

    fn request_headers(&self, body: &str) -> Vec<(String, String)> {
        let mut headers = vec![("User-Agent".to_owned(), self.user_agent.clone())];
        if !self.request_content_type.is_empty() && !body.is_empty() {
            headers.push(("Content-Type".to_owned(), self.request_content_type.clone()));
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
        if !looks_like_challenge(self.last_request.status, &self.response_body) {
            return Ok(false);
        }
        tracing::info!(url, "anti-bot challenge; solving");
        let solution = match solver.solve(url, self.client.cancel_token()) {
            Ok(solution) => solution,
            Err(FetchError::Cancelled) => return Err(Cancelled),
            Err(FetchError::Failed(why)) => {
                tracing::warn!(url, error = why, "flaresolverr failed");
                return Ok(false);
            }
        };
        self.response_body = solution.body;
        self.last_request.status = i64::from(solution.status);
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
