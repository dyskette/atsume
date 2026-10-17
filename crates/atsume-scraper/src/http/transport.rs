//! Sends one HTTP request and returns its response, without redirects or
//! cookies.
//!
//! Tests and the replay of recorded traffic plug into [`Transport`]. Redirects
//! and the cookie jar are handled above it, so they behave the same whichever
//! transport is in use.

use std::time::Duration;

use url::Url;

/// The largest response body that the default transport reads. It is well
/// above the size of any page or image, and it bounds how much memory a
/// hostile server can make atsume use.
const MAX_BODY: u64 = 64 * 1024 * 1024;

/// A request method that modules use.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Method {
    /// `GET`.
    Get,
    /// `POST`.
    Post,
    /// `HEAD`.
    Head,
}

impl Method {
    fn as_str(self) -> &'static str {
        match self {
            Self::Get => "GET",
            Self::Post => "POST",
            Self::Head => "HEAD",
        }
    }
}

/// One request.
#[derive(Clone, Debug)]
pub struct Request {
    /// The request method.
    pub method: Method,
    /// The address to request.
    pub url: Url,
    /// Header names and values, in the order they are sent.
    pub headers: Vec<(String, String)>,
    /// The body, which is empty when the request has none.
    pub body: Vec<u8>,
}

/// One response.
#[derive(Clone, Debug, Default)]
pub struct Response {
    /// The status code.
    pub status: u16,
    /// Header names and values, as received.
    pub headers: Vec<(String, String)>,
    /// The body, already decompressed.
    pub body: Vec<u8>,
}

impl Response {
    /// Returns the values of every header named `name`, ignoring case.
    pub fn headers_named<'a>(&'a self, name: &'a str) -> impl Iterator<Item = &'a str> {
        self.headers
            .iter()
            .filter(move |(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// Why a request got no response at all, such as a name that did not resolve,
/// a refused connection or a timeout.
#[derive(Clone, Debug, PartialEq, Eq, thiserror::Error)]
#[error("{0}")]
pub struct TransportError(pub String);

/// Sends a single request and returns the response as received.
///
/// An implementation must not follow redirects or keep cookies, and it must
/// return error statuses as responses rather than as errors.
///
/// Each request runs on a thread of its own. When the scrape is cancelled, the
/// request is abandoned, but its thread lives on until `send` returns. `send`
/// must therefore give up after a timeout.
pub trait Transport: Send + Sync {
    /// Sends `request`.
    ///
    /// # Errors
    ///
    /// Fails only when no response arrived.
    fn send(&self, request: &Request) -> Result<Response, TransportError>;
}

/// The default transport, over [`ureq`].
pub struct UreqTransport {
    agent: ureq::Agent,
}

impl UreqTransport {
    /// Returns a transport whose requests time out after `timeout`. That is
    /// also how long an abandoned request can keep its thread.
    #[must_use]
    pub fn new(timeout: Duration) -> Self {
        let config = ureq::Agent::config_builder()
            .timeout_global(Some(timeout))
            .max_redirects(0)
            .max_redirects_will_error(false)
            .http_status_as_error(false)
            .build();
        Self {
            agent: ureq::Agent::new_with_config(config),
        }
    }

    fn run(&self, request: &Request) -> Result<ureq::http::Response<ureq::Body>, ureq::Error> {
        let mut builder = ureq::http::Request::builder()
            .method(request.method.as_str())
            .uri(request.url.as_str());
        for (name, value) in &request.headers {
            builder = builder.header(name, value);
        }
        if request.body.is_empty() {
            self.agent.run(builder.body(())?)
        } else {
            self.agent.run(builder.body(request.body.as_slice())?)
        }
    }
}

impl Transport for UreqTransport {
    fn send(&self, request: &Request) -> Result<Response, TransportError> {
        let failed = |e: ureq::Error| TransportError(e.to_string());
        let mut response = self.run(request).map_err(failed)?;
        let headers = response
            .headers()
            .iter()
            .map(|(name, value)| {
                (
                    name.to_string(),
                    String::from_utf8_lossy(value.as_bytes()).into_owned(),
                )
            })
            .collect();
        let body = response
            .body_mut()
            .with_config()
            .limit(MAX_BODY)
            .read_to_vec();
        Ok(Response {
            status: response.status().as_u16(),
            headers,
            body: body.map_err(failed)?,
        })
    }
}
