use std::sync::Mutex;
use std::time::Instant;

use super::*;
use crate::testing::open_module_with;
use url::Url;

use crate::{CancelToken, ErrorKind, Event, Session};

/// A transport that answers from a function and records every request.
struct Scripted<F> {
    answer: F,
    seen: Mutex<Vec<Request>>,
}

impl<F> Transport for Scripted<F>
where
    F: Fn(&Request, usize) -> Result<Response, TransportError> + Send + Sync,
{
    fn send(&self, request: &Request) -> Result<Response, TransportError> {
        let mut seen = self.seen.lock().unwrap();
        seen.push(request.clone());
        let count = seen.len();
        drop(seen);
        (self.answer)(request, count)
    }
}

fn scripted<F>(answer: F) -> Arc<Scripted<F>>
where
    F: Fn(&Request, usize) -> Result<Response, TransportError> + Send + Sync,
{
    Arc::new(Scripted {
        answer,
        seen: Mutex::new(Vec::new()),
    })
}

fn respond(status: u16, body: &str) -> Response {
    Response {
        status,
        headers: Vec::new(),
        body: body.as_bytes().to_vec(),
    }
}

fn with_headers(mut response: Response, headers: &[(&str, &str)]) -> Response {
    response.headers = headers
        .iter()
        .map(|&(n, v)| (n.to_owned(), v.to_owned()))
        .collect();
    response
}

fn config(transport: Arc<dyn Transport>) -> HttpConfig {
    let retry = RetryPolicy {
        backoff_step: Duration::ZERO,
        ..RetryPolicy::default()
    };
    HttpConfig {
        transport,
        retry,
        ..HttpConfig::default()
    }
}

/// Opens a module with no handlers, for evaluating `HTTP` calls directly.
fn open_session(transport: Arc<dyn Transport>) -> Session {
    open_module_with(
        &config(transport),
        CancelToken::new(),
        "NewWebsiteModule()",
        "",
    )
}

fn header<'a>(request: &'a Request, name: &str) -> Option<&'a str> {
    request
        .headers
        .iter()
        .find(|(n, _)| n.eq_ignore_ascii_case(name))
        .map(|(_, v)| v.as_str())
}

#[test]
fn get_records_the_response() {
    let transport = scripted(|_, _| Ok(respond(200, "<b>payload</b>")));
    let session = open_session(transport.clone());
    assert_eq!(
        session.eval("tostring(HTTP.GET('https://a.example/x'))"),
        "true"
    );
    assert_eq!(session.eval("HTTP.ResultCode"), "200");
    assert_eq!(session.eval("HTTP.LastURL"), "https://a.example/x");
    assert_eq!(session.eval("HTTP.Document.ToString()"), "<b>payload</b>");
    assert_eq!(
        session.eval("#HTTP.Document .. '|' .. HTTP.Document.Size"),
        "14|14"
    );
    assert_eq!(session.eval("tostring(HTTP.Document)"), "<b>payload</b>");

    let seen = transport.seen.lock().unwrap();
    assert_eq!(header(&seen[0], "User-Agent"), Some(DEFAULT_USER_AGENT));
    assert_eq!(seen[0].method, Method::Get);
}

#[test]
fn request_methods_send_what_upstream_sends() {
    let transport = scripted(|_, _| Ok(respond(200, "ok")));
    let session = open_session(transport.clone());
    session.exec("HTTP.POST('https://a.example/form', 'q=1')");
    session.exec("HTTP.Reset(); HTTP.XHR('https://a.example/api')");
    session.exec(
        "HTTP.Reset(); HTTP.Headers.Values['User-Agent'] = 'Mine'; HTTP.HEAD('https://a.example/')",
    );

    let seen = transport.seen.lock().unwrap();
    assert_eq!(seen[0].method, Method::Post);
    assert_eq!(seen[0].body, b"q=1");
    assert_eq!(
        header(&seen[0], "Content-Type"),
        Some("application/x-www-form-urlencoded")
    );
    assert_eq!(header(&seen[1], "X-Requested-With"), Some("XMLHttpRequest"));
    assert_eq!(seen[2].method, Method::Head);
    assert_eq!(header(&seen[2], "User-Agent"), Some("Mine"));
}

#[test]
fn a_refusal_is_final_but_a_server_error_is_retried() {
    let refused = scripted(|_, _| Ok(respond(404, "missing")));
    let session_404 = open_session(refused.clone());
    assert_eq!(
        session_404.eval("tostring(HTTP.GET('https://a.example/'))"),
        "false"
    );
    assert_eq!(refused.seen.lock().unwrap().len(), 1);

    let failing = scripted(|_, n| {
        if n < 3 {
            Ok(respond(500, ""))
        } else {
            Ok(respond(200, "ok"))
        }
    });
    let session_500 = open_session(failing.clone());
    assert_eq!(
        session_500.eval("tostring(HTTP.GET('https://a.example/'))"),
        "true"
    );
    assert_eq!(failing.seen.lock().unwrap().len(), 3);
}

#[test]
fn no_answer_is_recorded_and_retried() {
    let unreachable = scripted(|_, _| Err(TransportError("connection refused".to_owned())));
    let session = open_session(unreachable.clone());
    session.exec("HTTP.RetryCount = 1");
    assert_eq!(
        session.eval("tostring(HTTP.GET('https://a.example/p'))"),
        "false"
    );
    assert_eq!(unreachable.seen.lock().unwrap().len(), 2);
    let last = session.last_request();
    assert_eq!(last.status, 0);
    assert_eq!(last.error.as_deref(), Some("connection refused"));
    assert_eq!(last.requested_url, "https://a.example/p");
}

#[test]
fn redirects_are_followed_and_a_move_is_recorded() {
    let transport = scripted(|request, _| match request.url.as_str() {
        "https://old.example/form" => Ok(with_headers(
            respond(302, ""),
            &[
                ("Location", "https://new.example/landing"),
                ("Set-Cookie", "hop=1; Path=/"),
            ],
        )),
        _ => Ok(respond(200, "landed")),
    });
    let session = open_session(transport.clone());
    session.exec("HTTP.Cookies.Values['mine'] = 'yes'");
    assert_eq!(
        session.eval("tostring(HTTP.POST('https://old.example/form', 'a=1'))"),
        "true"
    );
    assert_eq!(session.eval("HTTP.LastURL"), "https://new.example/landing");
    let moved = session.last_request().moved;
    assert_eq!(
        moved,
        Some(Moved {
            from_host: "old.example".to_owned(),
            to_origin: "https://new.example".to_owned(),
        })
    );

    let seen = transport.seen.lock().unwrap();
    // A 302 answering a POST continues as a GET without the body.
    assert_eq!(
        (seen[1].method, seen[1].body.is_empty()),
        (Method::Get, true)
    );
    // The module's own cookie stays with the host it was set for.
    assert_eq!(header(&seen[0], "Cookie"), Some("mine=yes"));
    assert_eq!(header(&seen[1], "Cookie"), None);
}

#[test]
fn cookies_travel_both_ways() {
    let transport = scripted(|_, _| {
        Ok(with_headers(
            respond(200, "ok"),
            &[("Set-Cookie", "session=abc123; Path=/")],
        ))
    });
    let session = open_session(transport.clone());
    session.exec("HTTP.Cookies.Values['ageGatePass'] = 'True'");
    session.exec("HTTP.GET('https://a.example/')");
    assert_eq!(session.eval("HTTP.Cookies.Values['session']"), "abc123");
    session.exec("HTTP.GET('https://a.example/again')");

    let seen = transport.seen.lock().unwrap();
    assert_eq!(header(&seen[0], "Cookie"), Some("ageGatePass=True"));
    // The jar sends the session once, though the module's list holds it too.
    assert_eq!(
        header(&seen[1], "Cookie"),
        Some("session=abc123; ageGatePass=True")
    );
    drop(seen);

    session.exec("MODULE.ClearCookies()");
    session.exec("HTTP.GET('https://a.example/cleared')");
    assert_eq!(header(&transport.seen.lock().unwrap()[2], "Cookie"), None);
}

#[test]
fn cancelling_ends_a_module_looping_over_requests() {
    let cancel = CancelToken::new();
    let canceller = cancel.clone();
    let transport = scripted(move |_, n| {
        if n == 3 {
            canceller.cancel();
        }
        Ok(respond(200, "ok"))
    });
    let init = "local m = NewWebsiteModule(); m.Name = 'Pager'; m.OnGetInfo = 'List'";
    let rest = "function List() local n = 0 while true do HTTP.GET('https://a.example/' .. n); n = n + 1 end end";
    let session = open_module_with(&config(transport.clone()), cancel, init, rest);
    let err = session.call_handler(Event::ReadSeries).unwrap_err();
    assert!(err.kind() == ErrorKind::Cancelled, "{err}");
    assert_eq!(transport.seen.lock().unwrap().len(), 3);
}

#[test]
fn cancelling_abandons_a_request_in_flight() {
    let cancel = CancelToken::new();
    let transport = scripted(|_, _| {
        std::thread::sleep(Duration::from_secs(5));
        Ok(respond(200, "too late"))
    });
    let init = "local m = NewWebsiteModule(); m.Name = 'Slow'; m.OnGetInfo = 'Fetch'";
    let rest = "function Fetch() return HTTP.GET('https://slow.example/') end";
    let session = open_module_with(&config(transport), cancel.clone(), init, rest);
    std::thread::spawn(move || {
        std::thread::sleep(Duration::from_millis(50));
        cancel.cancel();
    });
    let start = Instant::now();
    let err = session.call_handler(Event::ReadSeries).unwrap_err();
    assert!(err.kind() == ErrorKind::Cancelled, "{err}");
    assert!(
        start.elapsed() < Duration::from_secs(2),
        "{:?}",
        start.elapsed()
    );
}

const SOLVED: &str = r#"{"status":"ok","solution":{"url":"https://cf.example/","status":200,
    "response":"<b>real content</b>","userAgent":"SolvedAgent/1.0",
    "cookies":[{"name":"cf_clearance","value":"token123"}]}}"#;

fn challenged_site(body: &'static str) -> Arc<dyn Transport> {
    scripted(move |_, _| Ok(respond(403, body)))
}

fn open_session_with_solver(site: Arc<dyn Transport>, solver: Arc<dyn Transport>) -> Session {
    let flaresolverr = Flaresolverr::with_transport("http://solver:8191", solver).unwrap();
    let http = HttpConfig {
        solver: Some(Arc::new(flaresolverr)),
        ..config(site)
    };
    open_module_with(&http, CancelToken::new(), "NewWebsiteModule()", "")
}

#[test]
fn flaresolverr_recovers_a_challenged_page() {
    let solver = scripted(|_, _| Ok(respond(200, SOLVED)));
    let site = challenged_site("<title>Just a moment...</title>");
    let session = open_session_with_solver(site, solver.clone());
    assert_eq!(
        session.eval("tostring(HTTP.GET('https://cf.example/'))"),
        "true"
    );
    assert_eq!(
        session.eval("HTTP.Document.ToString()"),
        "<b>real content</b>"
    );
    // The clearance cookie and its agent carry forward together.
    assert_eq!(
        session.eval("HTTP.Cookies.Values['cf_clearance']"),
        "token123"
    );
    assert_eq!(session.eval("HTTP.UserAgent"), "SolvedAgent/1.0");
    assert!(session.last_request().challenged);

    let seen = solver.seen.lock().unwrap();
    assert_eq!(seen.len(), 1);
    assert_eq!(seen[0].url.as_str(), "http://solver:8191/v1");
}

#[test]
fn flaresolverr_is_not_used_for_an_ordinary_refusal() {
    let solver = scripted(|_, _| Ok(respond(200, SOLVED)));
    let session = open_session_with_solver(challenged_site("<h1>Forbidden</h1>"), solver.clone());
    assert_eq!(
        session.eval("tostring(HTTP.GET('https://cf.example/'))"),
        "false"
    );
    assert_eq!(
        solver.seen.lock().unwrap().len(),
        0,
        "the solver was called"
    );
}

#[test]
fn image_hooks_use_the_http_object() {
    let transport = scripted(|_, _| Ok(respond(200, "IMAGE")));
    let init = "local m = NewWebsiteModule(); m.Name = 'Img'
        m.OnBeforeDownloadImage = 'Before'; m.OnDownloadImage = 'Download'";
    let rest =
        "function Before() HTTP.Headers.Values['Referer'] = MODULE.RootURL .. 'r'; return true end
        function Download() return HTTP.GET(URL) end";
    let mut session = open_module_with(&config(transport), CancelToken::new(), init, rest);
    let headers = session
        .before_download_image("https://img.example/1.jpg")
        .unwrap();
    assert_eq!(headers.get("Referer").map(String::as_str), Some("r"));
    assert_eq!(
        session.download_image("https://img.example/1.jpg").unwrap(),
        b"IMAGE"
    );
}

/// Serves `respond` for every request on a local port until the test ends.
fn local_server(respond: fn(&tiny_http::Request) -> tiny_http::ResponseBox) -> String {
    let server = tiny_http::Server::http("127.0.0.1:0").unwrap();
    let address = format!("http://{}", server.server_addr().to_ip().unwrap());
    std::thread::spawn(move || {
        for request in server.incoming_requests() {
            let response = respond(&request);
            let _ = request.respond(response);
        }
    });
    address
}

#[test]
fn the_default_transport_returns_responses_as_received() {
    let base = local_server(|request| {
        let header = |text: &str| text.parse::<tiny_http::Header>().unwrap();
        match request.url() {
            "/moved" => tiny_http::Response::empty(301)
                .with_header(header("Location: /elsewhere"))
                .boxed(),
            "/missing" => tiny_http::Response::from_string("gone")
                .with_status_code(404)
                .boxed(),
            _ => tiny_http::Response::from_string("hello")
                .with_header(header("Set-Cookie: a=1"))
                .boxed(),
        }
    });
    let transport = UreqTransport::new(Duration::from_secs(5));
    let get = |path: &str| {
        let url = Url::parse(&format!("{base}{path}")).unwrap();
        transport
            .send(&Request {
                method: Method::Get,
                url,
                headers: Vec::new(),
                body: Vec::new(),
            })
            .unwrap()
    };

    let ok = get("/");
    assert_eq!((ok.status, ok.body.as_slice()), (200, b"hello".as_slice()));
    assert_eq!(ok.headers_named("set-cookie").collect::<Vec<_>>(), ["a=1"]);
    // Redirects and error statuses are left to the client above.
    assert_eq!(get("/moved").status, 301);
    assert_eq!(get("/missing").status, 404);
}

#[test]
fn the_session_works_without_lua() {
    let transport = scripted(|_, n| {
        if n < 2 {
            return Ok(respond(503, ""));
        }
        Ok(with_headers(respond(200, "ok"), &[("Set-Cookie", "s=1")]))
    });
    let mut session = HttpSession::new(&config(transport.clone()), CancelToken::new());
    assert_eq!(
        session.send(Method::Get, "https://a.example/", ""),
        Ok(true)
    );
    assert_eq!(session.response_body, b"ok");
    assert_eq!(session.cookies.value_of("s"), "1");
    assert_eq!(transport.seen.lock().unwrap().len(), 2);
}

#[test]
fn unknown_fields_read_as_nil_and_ignore_writes() {
    let session = open_session(scripted(|_, _| Ok(respond(200, ""))));
    session.exec("HTTP.Nonexistent = 1; MODULE.Nonexistent = 1; MODULE.Account.Nonexistent = 1");
    let read = "tostring(HTTP.Nonexistent) .. tostring(MODULE.Nonexistent) .. tostring(MODULE.Account.Nonexistent)";
    assert_eq!(session.eval(read), "nilnilnil");
    // Declared fields still read and write through to the state.
    session.exec("HTTP.UserAgent = 'Mine'; MODULE.Account.Status = asValid");
    assert_eq!(
        session.eval("HTTP.UserAgent .. MODULE.Account.Status"),
        "Mine1"
    );
}

#[test]
fn the_retry_policy_sets_how_often_a_request_is_tried() {
    let failing = scripted(|_, _| Ok(respond(500, "")));
    let once = HttpConfig {
        retry: RetryPolicy::NONE,
        ..config(failing.clone())
    };
    let session = open_module_with(&once, CancelToken::new(), "NewWebsiteModule()", "");
    assert_eq!(
        session.eval("tostring(HTTP.GET('https://a.example/'))"),
        "false"
    );
    assert_eq!(failing.seen.lock().unwrap().len(), 1);
    // A module may still ask for more for its own requests.
    session.exec("HTTP.RetryCount = 2; HTTP.GET('https://a.example/')");
    assert_eq!(failing.seen.lock().unwrap().len(), 4);
}

#[test]
fn a_reported_network_problem_carries_the_last_request() {
    let transport = scripted(|_, _| Ok(respond(404, "missing")));
    let init = "local m = NewWebsiteModule(); m.Name = 'Net'; m.OnGetInfo = 'Info'";
    let rest = "function Info() if not HTTP.GET('https://a.example' .. URL) then return net_problem end end";
    let mut session = open_module_with(&config(transport), CancelToken::new(), init, rest);
    let err = session.series_info("/series/x").unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Network);
    let request = err.last_request().unwrap();
    assert_eq!(
        (request.status, request.requested_url.as_str()),
        (404, "https://a.example/series/x")
    );
    assert_eq!(
        err.to_string(),
        "Net: network problem fetching /series/x (last status 404 from https://a.example/series/x)"
    );
}

#[test]
fn a_page_handler_reporting_net_problem_is_a_network_error() {
    let transport = scripted(|_, _| Ok(respond(503, "")));
    let init = "local m = NewWebsiteModule(); m.Name = 'Net'; m.OnGetPageNumber = 'Pages'";
    let rest = "function Pages() if not HTTP.GET('https://a.example' .. URL) then return net_problem end return no_error end";
    let mut session = open_module_with(&config(transport), CancelToken::new(), init, rest);
    let err = session.pages("/chapter/1").unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Network, "{err}");
    assert_eq!(err.last_request().unwrap().status, 503);
}

#[test]
fn a_listing_reporting_net_problem_names_the_page() {
    let transport = scripted(|_, _| Ok(respond(404, "")));
    let init = "local m = NewWebsiteModule(); m.Name = 'Net'; m.OnGetNameAndLink = 'List'";
    let rest = "function List() if not HTTP.GET('https://a.example/page/' .. (URL + 1)) then return net_problem end return no_error end";
    let mut session = open_module_with(&config(transport), CancelToken::new(), init, rest);
    let err = session.directory_page(0, 2).unwrap_err();
    assert_eq!(err.kind(), ErrorKind::Network);
    assert_eq!(
        err.to_string(),
        "Net: network problem listing page 3 (last status 404 from https://a.example/page/3)"
    );
}
