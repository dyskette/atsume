//! The `HTTP` object modules fetch pages with.
//!
//! One is bound per runner, so cookies and headers persist across the
//! handler calls of one scrape, as modules expect. A request reports success
//! as a boolean, which is what handlers test, and records the outcome on the
//! object for the module to read.

mod client;
mod flaresolverr;
mod limiter;
mod transport;

use std::cell::RefCell;
use std::rc::Rc;
use std::sync::Arc;
use std::time::Duration;

use mlua::{Function, Lua, Value};
use url::Url;

pub use flaresolverr::Flaresolverr;
pub use limiter::{HostLimiter, Limiter, Unlimited};
pub use transport::{Method, Request, Response, Transport, TransportError, UreqTransport};

use self::client::{Client, Exchange, FetchError};
use self::flaresolverr::{has_challenge_markers, looks_like_challenge};
use crate::budget::{CancelToken, Cancelled};
use crate::document::{Document, DocumentHandle};
use crate::object::{Field, Object};
use crate::sandbox::text_of;
use crate::strings::{StringList, StringsHandle, bad_argument};

/// The user agent FMD2 sends. Some sites serve different markup to an agent
/// that looks automated.
pub const DEFAULT_USER_AGENT: &str = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) \
    AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36";

/// How long one request may take before it counts as failed.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(60);

/// How the runners of a [`Host`](crate::Host) reach the network.
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

/// The state behind `HTTP`, shared between the runner and Lua.
pub(crate) struct Http {
    client: Client,
    solver: Option<Arc<Flaresolverr>>,
    backoff: Duration,
    pub(crate) headers: StringList,
    pub(crate) cookies: StringList,
    pub(crate) document: Document,
    mime_type: String,
    user_agent: String,
    retry_count: i64,
    terminated: bool,
    pub(crate) last: LastRequest,
}

impl Http {
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
            headers: StringList::default(),
            cookies: StringList::default(),
            document: Document::default(),
            mime_type: String::new(),
            user_agent: DEFAULT_USER_AGENT.to_owned(),
            retry_count: 2,
            terminated: false,
            last: LastRequest::default(),
        }
    }

    /// Clears what one request set up, keeping cookies.
    fn reset(&mut self) {
        self.headers.borrow_mut().clear();
        self.mime_type.clear();
        self.document.borrow_mut().clear();
        self.last.status = 0;
    }

    pub(crate) fn clear_cookies(&mut self) {
        self.cookies.borrow_mut().clear();
        self.client.clear_cookies();
    }

    /// Requests `url`, retrying a failure that a retry might fix, and reports
    /// whether it succeeded.
    fn request(&mut self, method: Method, url: &str, body: &str) -> Result<bool, Cancelled> {
        self.document.borrow_mut().clear();
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
                Err(FetchError::Cancelled) => return Err(Cancelled),
                Err(FetchError::Failed(why)) => self.last.error = Some(why),
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
    fn attempt(&mut self, method: Method, url: &str, body: &str) -> Result<bool, FetchError> {
        let parsed = Url::parse(url).map_err(|e| FetchError::Failed(format!("{url}: {e}")))?;
        let request = Request {
            method,
            url: parsed.clone(),
            headers: self.request_headers(body),
            body: body.as_bytes().to_vec(),
        };
        let cookies = self.cookies.borrow().items().to_vec();
        let Exchange {
            url: final_url,
            response,
        } = self.client.fetch(request, &cookies)?;

        self.last.status = i64::from(response.status);
        self.last.url = final_url.to_string();
        if self.last.moved.is_none() && final_url.host_str() != parsed.host_str() {
            let to = final_url.origin().ascii_serialization();
            self.last.moved = Some((parsed.host_str().unwrap_or_default().to_owned(), to));
        }
        // Modules read HTTP.Cookies after a login, to see whether the server
        // issued the session cookie they need.
        for header in response.headers_named("Set-Cookie") {
            if let Ok(cookie) = cookie::Cookie::parse(header) {
                self.cookies
                    .borrow_mut()
                    .set_value(cookie.name(), cookie.value());
            }
        }
        self.last.challenged |= has_challenge_markers(&response.body);
        *self.document.borrow_mut() = response.body;
        Ok((200..400).contains(&response.status))
    }

    fn request_headers(&self, body: &str) -> Vec<(String, String)> {
        let mut headers = vec![("User-Agent".to_owned(), self.user_agent.clone())];
        if !self.mime_type.is_empty() && !body.is_empty() {
            headers.push(("Content-Type".to_owned(), self.mime_type.clone()));
        }
        for pair in self.headers.borrow().items() {
            let Some((name, value)) = pair.split_once('=') else {
                continue;
            };
            let name = name.trim();
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
        if !looks_like_challenge(self.last.status, &self.document.borrow()) {
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
        *self.document.borrow_mut() = solution.body;
        self.last.status = i64::from(solution.status);
        // The clearance cookies are bound to the agent that earned them, so
        // both carry forward, or the next request is challenged again.
        if !solution.user_agent.is_empty() {
            self.user_agent = solution.user_agent;
        }
        for (name, value) in &solution.cookies {
            self.cookies.borrow_mut().set_value(name, value);
        }
        Ok((200..400).contains(&solution.status))
    }
}

/// Builds the `HTTP` object over `http`.
pub(crate) fn bind(lua: &Lua, http: &Rc<RefCell<Http>>) -> mlua::Result<Value> {
    let (headers, cookies, document) = {
        let state = http.borrow();
        (
            Rc::clone(&state.headers),
            Rc::clone(&state.cookies),
            Rc::clone(&state.document),
        )
    };
    let mut object = Object::new(Rc::clone(http))
        .field("MimeType", Field::Text(|h| &mut h.mime_type))
        .field("UserAgent", Field::Text(|h| &mut h.user_agent))
        .field("LastURL", Field::Text(|h| &mut h.last.url))
        .field("RetryCount", Field::Int(|h| &mut h.retry_count))
        .field("ResultCode", Field::Int(|h| &mut h.last.status))
        .field("Terminated", Field::Bool(|h| &mut h.terminated))
        .field(
            "Headers",
            Field::Constant(Value::UserData(
                lua.create_userdata(StringsHandle(headers))?,
            )),
        )
        .field(
            "Cookies",
            Field::Constant(Value::UserData(
                lua.create_userdata(StringsHandle(cookies))?,
            )),
        )
        .field(
            "Document",
            Field::Constant(Value::UserData(
                lua.create_userdata(DocumentHandle(document))?,
            )),
        );
    for (name, function) in methods(lua, http)? {
        object = object.field(name, Field::Constant(Value::Function(function)));
    }
    object.into_value(lua)
}

/// Prepares the state before a request method sends.
type Prepare = fn(&mut Http);

fn methods(lua: &Lua, http: &Rc<RefCell<Http>>) -> mlua::Result<Vec<(&'static str, Function)>> {
    let mut methods = vec![
        ("GET", request_method(lua, http, Method::Get, |_| {})?),
        ("HEAD", request_method(lua, http, Method::Head, |_| {})?),
        (
            "POST",
            request_method(lua, http, Method::Post, |h| {
                if h.mime_type.is_empty() {
                    "application/x-www-form-urlencoded".clone_into(&mut h.mime_type);
                }
            })?,
        ),
        // A GET that announces itself as an in-page request, which some
        // servers answer differently.
        (
            "XHR",
            request_method(lua, http, Method::Get, |h| {
                h.headers
                    .borrow_mut()
                    .set_value("X-Requested-With", "XMLHttpRequest");
            })?,
        ),
        ("Reset", action(lua, http, Http::reset)?),
        ("ResetBasic", action(lua, http, Http::reset)?),
        ("ClearCookies", action(lua, http, Http::clear_cookies)?),
        // Proxying is the host's policy, not the module's.
        ("SetProxy", action(lua, http, |_| {})?),
    ];
    let cookies = Rc::clone(&http.borrow().cookies);
    methods.push((
        "GetCookies",
        lua.create_function(move |_, ()| Ok(cookies.borrow().items().join("; ")))?,
    ));
    methods.push((
        "AddServerCookies",
        add_server_cookies(lua, &http.borrow().cookies)?,
    ));
    Ok(methods)
}

/// Builds a request method: the URL first, then a body for `POST`.
///
/// It refuses to start once the scrape is cancelled, so a module looping over
/// requests ends at the next one instead of carrying on with failed ones.
fn request_method(
    lua: &Lua,
    http: &Rc<RefCell<Http>>,
    method: Method,
    prepare: Prepare,
) -> mlua::Result<Function> {
    let http = Rc::clone(http);
    lua.create_function(move |lua, (url, body): (Value, Value)| {
        let url = text_of(lua, &url).ok_or_else(|| bad_argument(1, "string", &url))?;
        let body = if method == Method::Post {
            text_of(lua, &body).unwrap_or_default()
        } else {
            String::new()
        };
        let mut http = http.borrow_mut();
        prepare(&mut http);
        let sent = http
            .client
            .cancel_token()
            .check()
            .and_then(|()| http.request(method, &url, &body));
        sent.map_err(|cancelled| {
            http.terminated = true;
            mlua::Error::runtime(cancelled)
        })
    })
}

fn action(lua: &Lua, http: &Rc<RefCell<Http>>, act: fn(&mut Http)) -> mlua::Result<Function> {
    let http = Rc::clone(http);
    lua.create_function(move |_, ()| {
        act(&mut http.borrow_mut());
        Ok(())
    })
}

/// Builds `AddServerCookies(url, cookie)`. Upstream takes the cookie second.
pub(crate) fn add_server_cookies(lua: &Lua, cookies: &StringList) -> mlua::Result<Function> {
    let cookies = Rc::clone(cookies);
    lua.create_function(move |lua, (_, cookie): (Value, Value)| {
        let cookie = text_of(lua, &cookie).ok_or_else(|| bad_argument(2, "string", &cookie))?;
        cookies.borrow_mut().add(cookie);
        Ok(())
    })
}

#[cfg(test)]
mod tests;
