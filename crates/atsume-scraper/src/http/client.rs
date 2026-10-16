//! Redirects, cookies and rate limiting on top of a [`Transport`].

use std::sync::{Arc, mpsc};

use cookie_store::CookieStore;
use url::Url;

use super::limiter::Limiter;
use super::transport::{Method, Request, Response, Transport};
use crate::budget::{CANCEL_POLL, CancelToken, Cancelled};

/// How many redirects one request may follow, as browsers allow.
const MAX_REDIRECTS: usize = 10;

/// Why a fetch ended without a response.
#[derive(Clone, Debug, PartialEq, Eq)]
pub(crate) enum FetchError {
    /// The scrape was cancelled.
    Cancelled,
    /// No response arrived, or the redirects did not end.
    Failed(String),
}

impl From<Cancelled> for FetchError {
    fn from(_: Cancelled) -> Self {
        Self::Cancelled
    }
}

/// The response a fetch ended at, and the address it came from.
pub(crate) struct Exchange {
    pub(crate) url: Url,
    pub(crate) response: Response,
}

/// A client with its own cookie jar, for the requests of one scrape.
pub(crate) struct Client {
    transport: Arc<dyn Transport>,
    limiter: Arc<dyn Limiter>,
    cancel: CancelToken,
    jar: CookieStore,
}

impl Client {
    pub(crate) fn new(
        transport: Arc<dyn Transport>,
        limiter: Arc<dyn Limiter>,
        cancel: CancelToken,
    ) -> Self {
        Self {
            transport,
            limiter,
            cancel,
            jar: CookieStore::default(),
        }
    }

    pub(crate) fn cancel_token(&self) -> &CancelToken {
        &self.cancel
    }

    pub(crate) fn clear_cookies(&mut self) {
        self.jar.clear();
    }

    /// Sends `request`, following redirects, and returns the final response.
    ///
    /// `cookies` are the `name=value` pairs the module set itself. They go
    /// with the jar's cookies, but only to the requested host and its
    /// subdomains, so a redirect elsewhere does not carry them off.
    pub(crate) fn fetch(
        &mut self,
        mut request: Request,
        cookies: &[String],
    ) -> Result<Exchange, FetchError> {
        let origin = request.url.host_str().unwrap_or_default().to_owned();
        for _ in 0..=MAX_REDIRECTS {
            let module_cookies = if same_site(&origin, &request.url) {
                cookies
            } else {
                &[]
            };
            let response = self.send(&request, module_cookies)?;
            let Some(next) = redirect(&request, &response) else {
                return Ok(Exchange {
                    url: request.url,
                    response,
                });
            };
            request = next;
        }
        Err(FetchError::Failed(format!(
            "stopped after {MAX_REDIRECTS} redirects"
        )))
    }

    fn send(
        &mut self,
        request: &Request,
        module_cookies: &[String],
    ) -> Result<Response, FetchError> {
        self.limiter
            .wait(request.url.host_str().unwrap_or_default(), &self.cancel)?;
        let mut request = request.clone();
        if let Some(header) = self.cookie_header(&request.url, module_cookies) {
            request.headers.push(("Cookie".to_owned(), header));
        }
        let response = send_cancellable(&self.transport, request.clone(), &self.cancel)?;
        let received = response
            .headers_named("Set-Cookie")
            .filter_map(|header| cookie::Cookie::parse(header.to_owned()).ok());
        self.jar.store_response_cookies(received, &request.url);
        Ok(response)
    }

    /// Builds the `Cookie` header: the jar's cookies for `url`, then the
    /// module's own, skipping any the jar already sends.
    fn cookie_header(&self, url: &Url, module_cookies: &[String]) -> Option<String> {
        let mut pairs: Vec<String> = self
            .jar
            .get_request_values(url)
            .map(|(name, value)| format!("{name}={value}"))
            .collect();
        let jar_names: Vec<String> = pairs
            .iter()
            .filter_map(|p| p.split_once('='))
            .map(|(n, _)| n.to_owned())
            .collect();
        let module_only = module_cookies.iter().filter(|pair| {
            let name = pair.split_once('=').map_or(pair.as_str(), |(n, _)| n);
            !jar_names.iter().any(|jar_name| jar_name == name)
        });
        pairs.extend(module_only.cloned());
        (!pairs.is_empty()).then(|| pairs.join("; "))
    }
}

/// Reports whether `url` is on `origin` or one of its subdomains.
fn same_site(origin: &str, url: &Url) -> bool {
    let host = url.host_str().unwrap_or_default();
    host.eq_ignore_ascii_case(origin) || host.to_ascii_lowercase().ends_with(&format!(".{origin}"))
}

/// Returns the request a redirect response points to, or `None` if the
/// response is not a redirect.
///
/// As browsers do, a 303, or a 301 or 302 answering a POST, continues as a
/// GET without the body.
fn redirect(request: &Request, response: &Response) -> Option<Request> {
    if !matches!(response.status, 301 | 302 | 303 | 307 | 308) {
        return None;
    }
    let location = response.headers_named("Location").next()?;
    let url = request.url.join(location).ok()?;
    let becomes_get = response.status == 303 && request.method != Method::Head
        || matches!(response.status, 301 | 302) && request.method == Method::Post;
    let mut next = Request {
        url,
        ..request.clone()
    };
    if becomes_get {
        next.method = Method::Get;
        next.body.clear();
        next.headers
            .retain(|(name, _)| !name.eq_ignore_ascii_case("Content-Type"));
    }
    Some(next)
}

/// Sends `request` on its own thread and waits for it, giving up as soon as
/// the scrape is cancelled.
///
/// An abandoned request finishes in the background and its response is
/// dropped, so a cancelled scrape never waits out a slow site.
pub(crate) fn send_cancellable(
    transport: &Arc<dyn Transport>,
    request: Request,
    cancel: &CancelToken,
) -> Result<Response, FetchError> {
    let (sender, receiver) = mpsc::channel();
    let transport = Arc::clone(transport);
    std::thread::spawn(move || {
        // The receiver is gone if the scrape was cancelled; nothing to do then.
        let _ = sender.send(transport.send(&request));
    });
    loop {
        match receiver.recv_timeout(CANCEL_POLL) {
            Ok(result) => return result.map_err(|e| FetchError::Failed(e.0)),
            Err(mpsc::RecvTimeoutError::Timeout) => cancel.check()?,
            Err(mpsc::RecvTimeoutError::Disconnected) => {
                return Err(FetchError::Failed(
                    "the request thread ended early".to_owned(),
                ));
            }
        }
    }
}
