//! Getting past anti-bot interstitials through a FlareSolverr instance.

use std::sync::Arc;
use std::time::Duration;

use serde::Deserialize;
use url::Url;

use super::client::{FetchError, send_cancellable};
use super::transport::{Method, Request, Transport, UreqTransport};
use crate::budget::CancelToken;

/// Strings an interstitial serves. A refusal alone is not enough: most 403s
/// are ordinary, and solving one would waste a browser run.
const CHALLENGE_MARKERS: [&str; 7] = [
    "Just a moment",
    "cf-browser-verification",
    "cf_chl_opt",
    "_cf_chl_",
    "Checking your browser",
    "DDoS-Guard",
    "__ddg",
];

/// How much of a body is searched for markers. They appear early, and a
/// chapter page can be a megabyte.
const MARKER_WINDOW: usize = 8192;

/// Reports whether a response is an anti-bot interstitial worth solving.
pub(crate) fn looks_like_challenge(status: i64, body: &[u8]) -> bool {
    matches!(status, 403 | 503) && has_challenge_markers(body)
}

/// Reports whether a body carries an interstitial's markers, whatever its
/// status.
pub(crate) fn has_challenge_markers(body: &[u8]) -> bool {
    let head = &body[..body.len().min(MARKER_WINDOW)];
    CHALLENGE_MARKERS
        .iter()
        .any(|marker| head.windows(marker.len()).any(|w| w == marker.as_bytes()))
}

/// A FlareSolverr instance, consulted only after a request was refused.
pub struct Flaresolverr {
    endpoint: Url,
    transport: Arc<dyn Transport>,
}

/// What a solved challenge yields.
#[derive(Debug)]
pub(crate) struct Solution {
    pub(crate) body: Vec<u8>,
    pub(crate) status: u16,
    pub(crate) user_agent: String,
    /// The cookies that clear the challenge, as `(name, value)`.
    pub(crate) cookies: Vec<(String, String)>,
}

#[derive(Deserialize)]
struct Reply {
    status: String,
    #[serde(default)]
    message: String,
    solution: Option<ReplySolution>,
}

#[derive(Deserialize)]
#[serde(rename_all = "camelCase")]
struct ReplySolution {
    status: u16,
    response: String,
    #[serde(default)]
    user_agent: String,
    #[serde(default)]
    cookies: Vec<ReplyCookie>,
}

#[derive(Deserialize)]
struct ReplyCookie {
    name: String,
    value: String,
}

impl Flaresolverr {
    /// Returns a solver for the instance at `base`, such as
    /// `http://flaresolverr:8191`.
    ///
    /// # Errors
    ///
    /// Fails if `base` is not a valid URL.
    pub fn new(base: &str) -> Result<Self, url::ParseError> {
        // Solving runs a real browser, so it gets longer than a page fetch.
        let transport = Arc::new(UreqTransport::new(Duration::from_secs(120)));
        Self::with_transport(base, transport)
    }

    /// Returns a solver that reaches the instance through `transport`.
    ///
    /// # Errors
    ///
    /// Fails if `base` is not a valid URL.
    pub fn with_transport(
        base: &str,
        transport: Arc<dyn Transport>,
    ) -> Result<Self, url::ParseError> {
        let endpoint = Url::parse(&format!("{}/v1", base.trim_end_matches('/')))?;
        Ok(Self {
            endpoint,
            transport,
        })
    }

    /// Fetches `url` through the solver's browser.
    pub(crate) fn solve(&self, url: &str, cancel: &CancelToken) -> Result<Solution, FetchError> {
        let command = serde_json::json!({"cmd": "request.get", "url": url, "maxTimeout": 60_000});
        let request = Request {
            method: Method::Post,
            url: self.endpoint.clone(),
            headers: vec![("Content-Type".to_owned(), "application/json".to_owned())],
            body: command.to_string().into_bytes(),
        };
        let response = send_cancellable(&self.transport, request, cancel).map_err(|e| match e {
            FetchError::Failed(why) => {
                FetchError::Failed(format!("flaresolverr unreachable: {why}"))
            }
            cancelled @ FetchError::Cancelled => cancelled,
        })?;
        let reply: Reply = serde_json::from_slice(&response.body).map_err(|e| {
            FetchError::Failed(format!("flaresolverr returned unparseable JSON: {e}"))
        })?;
        let ("ok", Some(solution)) = (reply.status.as_str(), reply.solution) else {
            return Err(FetchError::Failed(format!(
                "flaresolverr: {}",
                reply.message
            )));
        };
        Ok(Solution {
            body: solution.response.into_bytes(),
            status: solution.status,
            user_agent: solution.user_agent,
            cookies: solution
                .cookies
                .into_iter()
                .map(|c| (c.name, c.value))
                .collect(),
        })
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn only_a_refusal_with_markers_is_a_challenge() {
        let cases = [
            (403, "<title>Just a moment...</title>", true),
            (503, "<div>DDoS-Guard</div>", true),
            (403, "window._cf_chl_opt = {}", true),
            (403, "<h1>Forbidden</h1>", false),
            (404, "Just a moment", false),
            (200, "Just a moment", false),
        ];
        for (status, body, want) in cases {
            assert_eq!(
                looks_like_challenge(status, body.as_bytes()),
                want,
                "{status} {body}"
            );
        }
    }
}
