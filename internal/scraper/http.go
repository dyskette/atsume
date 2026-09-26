package scraper

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	rt "github.com/arnodel/golua/runtime"
	"golang.org/x/net/publicsuffix"
)

// DefaultUserAgent matches what FMD2 sends, because a number of modules are
// served different markup when the agent looks automated.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Limiter gates outbound requests. The download worker supplies a per-host
// implementation so that a run across 600 sites stays polite.
type Limiter interface {
	Wait(ctx context.Context, host string) error
}

// nopLimiter is used in tests and when no limiter is configured.
type nopLimiter struct{}

func (nopLimiter) Wait(context.Context, string) error { return nil }

// HTTP is the THTTPSendThread object injected into every module. One instance
// is bound per Lua state, so cookies and headers persist across the handler
// calls of a single scrape the way modules expect.
type HTTP struct {
	ctx     context.Context
	client  *http.Client
	limiter Limiter
	solver  *Flaresolverr

	Headers    *Strings
	Cookies    *Strings
	MimeType   string
	UserAgent  string
	RetryCount int
	ResultCode int
	LastURL    string
	Terminated bool

	// LastErr is why the last request got no answer at all: a name that did
	// not resolve, a refused connection, a timeout. It is nil when the site
	// answered, whatever the status.
	LastErr error
	// Challenged is set once any response carried an anti-bot interstitial,
	// whatever its status, so a read that came back empty can be told apart
	// from one that was quietly turned away.
	Challenged bool
	// RedirectFrom and RedirectTo record the first request that a redirect
	// took to another host: the host asked for, and the address it ended at.
	// A site that moved domains usually says so this way.
	RedirectFrom, RedirectTo string

	// Document holds the last response body. It is a pointer so that a module
	// rewriting it in place — descrambling a tiled image, for instance — is
	// visible here afterwards.
	Document *Document
}

// NewHTTP builds a client with its own cookie jar.
//
// transport may be nil for the default. Tests supply a Cassette so a scrape
// replays recorded pages instead of reaching the network.
func NewHTTP(ctx context.Context, limiter Limiter, transport http.RoundTripper, solver *Flaresolverr) *HTTP {
	if limiter == nil {
		limiter = nopLimiter{}
	}
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &HTTP{
		ctx:       ctx,
		client:    &http.Client{Jar: jar, Timeout: 60 * time.Second, Transport: transport},
		limiter:   limiter,
		solver:    solver,
		Headers:   NewStrings(),
		Cookies:   NewStrings(),
		Document:  &Document{},
		UserAgent: DefaultUserAgent,

		RetryCount: 2,
	}
}

// do performs one request with retries, recording the outcome on the receiver.
// It returns false rather than an error because that is what Lua handlers test.
func (h *HTTP) do(method, rawURL, body string) bool {
	h.Document.Set(nil)
	h.ResultCode = 0
	h.LastErr = nil

	for attempt := 0; attempt <= h.RetryCount; attempt++ {
		if h.ctx.Err() != nil {
			h.Terminated = true
			return false
		}
		if attempt > 0 {
			// linear backoff: these are third-party sites, not an internal API
			select {
			case <-h.ctx.Done():
				h.Terminated = true
				return false
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		ok, err := h.attempt(method, rawURL, body)
		if ok {
			return true
		}
		h.LastErr = err
		// An anti-bot interstitial is recoverable where an ordinary refusal is
		// not, so it is checked before giving up on a 4xx.
		if h.trySolve(rawURL) {
			return true
		}
		// a 4xx is a real answer; retrying will not change it
		if h.ResultCode >= 400 && h.ResultCode < 500 {
			return false
		}
	}
	return false
}

func (h *HTTP) attempt(method, rawURL, body string) (bool, error) {
	req, err := http.NewRequestWithContext(h.ctx, method, rawURL, strings.NewReader(body))
	if err != nil {
		return false, err
	}
	if err := h.limiter.Wait(h.ctx, req.URL.Host); err != nil {
		return false, err
	}

	req.Header.Set("User-Agent", h.UserAgent)
	if h.MimeType != "" && body != "" {
		req.Header.Set("Content-Type", h.MimeType)
	}
	for _, raw := range h.Headers.All() {
		if k, v, ok := strings.Cut(raw, "="); ok {
			req.Header.Set(strings.TrimSpace(k), v)
		}
	}
	// Cookies the module set itself. The jar handles what a server sends back,
	// but a module assigning HTTP.Cookies.Values['ageGatePass'] expects that to
	// travel with the request, and silently dropping it yields a gate page
	// rather than an error.
	if cookies := h.Cookies.All(); len(cookies) > 0 {
		req.Header.Set("Cookie", strings.Join(cookies, "; "))
	}

	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	h.ResultCode = resp.StatusCode
	h.LastURL = resp.Request.URL.String()
	if final := resp.Request.URL; h.RedirectTo == "" && final.Host != req.URL.Host {
		h.RedirectFrom, h.RedirectTo = req.URL.Host, final.Scheme+"://"+final.Host
	}
	// Modules read HTTP.Cookies after a login to check whether the server
	// issued the session cookie they were looking for.
	for _, c := range resp.Cookies() {
		h.Cookies.SetValue(c.Name, c.Value)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	h.Document.Set(data)
	if hasChallengeMarkers(data) {
		h.Challenged = true
	}
	return resp.StatusCode >= 200 && resp.StatusCode < 400, nil
}

// Get fetches a page the way a module's HTTP.GET does: the same user agent,
// retries and anti-bot solving, with the outcome recorded on the receiver.
// It reports whether the request succeeded.
func (h *HTTP) Get(url string) bool { return h.do(http.MethodGet, url, "") }

// Reset clears per-request state but keeps cookies, matching HTTP.Reset().
func (h *HTTP) Reset() {
	h.Headers.Clear()
	h.MimeType = ""
	h.Document.Set(nil)
	h.ResultCode = 0
}

// bind exposes the client as HTTP. Every request method checks the
// runner's context first, so a cancelled scrape ends at its next request
// instead of carrying on with failed ones.
func (h *HTTP) bind(r *rt.Runtime, checkContext func(*rt.Thread)) rt.Value {
	f := newFields("atsume.HTTP")
	f.str["MimeType"] = &h.MimeType
	f.str["UserAgent"] = &h.UserAgent
	f.str["LastURL"] = &h.LastURL
	f.num["RetryCount"] = &h.RetryCount
	f.num["ResultCode"] = &h.ResultCode
	f.boolean["Terminated"] = &h.Terminated
	f.list["Headers"] = h.Headers
	f.list["Cookies"] = h.Cookies
	f.getter["Document"] = func(t *rt.Thread) rt.Value { return pushDocument(t.Runtime, h.Document) }

	// request adapts a request method: URL first, then an optional body.
	request := func(method string, before func()) goFn {
		return goFn{2, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			checkContext(t)
			u, err := checkString(c, 0)
			if err != nil {
				return nil, err
			}
			body := ""
			if method == http.MethodPost {
				if body, err = optString(c, 1, ""); err != nil {
					return nil, err
				}
			}
			if before != nil {
				before()
			}
			return c.PushingNext1(t.Runtime, rt.BoolValue(h.do(method, u, body))), nil
		}}
	}
	f.methods["GET"] = request(http.MethodGet, nil)
	f.methods["POST"] = request(http.MethodPost, func() {
		if h.MimeType == "" {
			h.MimeType = "application/x-www-form-urlencoded"
		}
	})
	f.methods["HEAD"] = request(http.MethodHead, nil)
	// XHR is a GET that announces itself as an in-page request; several
	// modules rely on the server varying its response on this header.
	f.methods["XHR"] = request(http.MethodGet, func() {
		h.Headers.SetValue("X-Requested-With", "XMLHttpRequest")
	})
	f.methods["Reset"] = noResult(0, func(*rt.GoCont) error { h.Reset(); return nil })
	f.methods["ResetBasic"] = noResult(0, func(*rt.GoCont) error { h.Reset(); return nil })
	f.methods["ClearCookies"] = noResult(0, func(*rt.GoCont) error { h.Cookies.Clear(); return nil })
	f.methods["GetCookies"] = goFn{0, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		return c.PushingNext1(t.Runtime, rt.StringValue(strings.Join(h.Cookies.All(), "; "))), nil
	}}
	// The cookie is the second argument, as upstream's signature has it.
	f.methods["AddServerCookies"] = noResult(2, func(c *rt.GoCont) error {
		s, err := checkString(c, 1)
		if err == nil {
			h.Cookies.Add(s)
		}
		return err
	})
	f.methods["SetProxy"] = noResult(0, func(*rt.GoCont) error { return nil }) // proxying is host policy
	return f.push(r)
}
