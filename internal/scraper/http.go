package scraper

import (
	"context"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"

	lua "github.com/yuin/gopher-lua"
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

	Headers    *Strings
	Cookies    *Strings
	MimeType   string
	UserAgent  string
	RetryCount int
	ResultCode int
	LastURL    string
	Terminated bool

	// Document holds the last response body. It is a pointer so that a module
	// rewriting it in place — descrambling a tiled image, for instance — is
	// visible here afterwards.
	Document *Document
}

// NewHTTP builds a client with its own cookie jar.
//
// transport may be nil for the default. Tests supply a Cassette so a scrape
// replays recorded pages instead of reaching the network.
func NewHTTP(ctx context.Context, limiter Limiter, transport http.RoundTripper) *HTTP {
	if limiter == nil {
		limiter = nopLimiter{}
	}
	jar, _ := cookiejar.New(&cookiejar.Options{PublicSuffixList: publicsuffix.List})
	return &HTTP{
		ctx:       ctx,
		client:    &http.Client{Jar: jar, Timeout: 60 * time.Second, Transport: transport},
		limiter:   limiter,
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

	var lastErr error
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
		lastErr = err
		// a 4xx is a real answer; retrying will not change it
		if h.ResultCode >= 400 && h.ResultCode < 500 {
			return false
		}
	}
	_ = lastErr
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

	resp, err := h.client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()

	h.ResultCode = resp.StatusCode
	h.LastURL = resp.Request.URL.String()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return false, err
	}
	h.Document.Set(data)
	return resp.StatusCode >= 200 && resp.StatusCode < 400, nil
}

// Reset clears per-request state but keeps cookies, matching HTTP.Reset().
func (h *HTTP) Reset() {
	h.Headers.Clear()
	h.MimeType = ""
	h.Document.Set(nil)
	h.ResultCode = 0
}

func (h *HTTP) bind(L *lua.LState) lua.LValue {
	f := newFields("atsume.HTTP")
	f.str["MimeType"] = &h.MimeType
	f.str["UserAgent"] = &h.UserAgent
	f.str["LastURL"] = &h.LastURL
	f.num["RetryCount"] = &h.RetryCount
	f.num["ResultCode"] = &h.ResultCode
	f.boolean["Terminated"] = &h.Terminated
	f.list["Headers"] = h.Headers
	f.list["Cookies"] = h.Cookies

	f.getter["Document"] = func(L *lua.LState) lua.LValue { return pushDocument(L, h.Document) }

	f.methods["GET"] = func(L *lua.LState) int {
		L.Push(lua.LBool(h.do(http.MethodGet, L.CheckString(1), "")))
		return 1
	}
	f.methods["POST"] = func(L *lua.LState) int {
		if h.MimeType == "" {
			h.MimeType = "application/x-www-form-urlencoded"
		}
		L.Push(lua.LBool(h.do(http.MethodPost, L.CheckString(1), L.OptString(2, ""))))
		return 1
	}
	f.methods["HEAD"] = func(L *lua.LState) int {
		L.Push(lua.LBool(h.do(http.MethodHead, L.CheckString(1), "")))
		return 1
	}
	// XHR is a GET that announces itself as an in-page request; several modules
	// rely on the server varying its response on this header.
	f.methods["XHR"] = func(L *lua.LState) int {
		h.Headers.SetValue("X-Requested-With", "XMLHttpRequest")
		L.Push(lua.LBool(h.do(http.MethodGet, L.CheckString(1), "")))
		return 1
	}
	f.methods["Reset"] = func(L *lua.LState) int { h.Reset(); return 0 }
	f.methods["ResetBasic"] = func(L *lua.LState) int { h.Reset(); return 0 }
	f.methods["ClearCookies"] = func(L *lua.LState) int { h.Cookies.Clear(); return 0 }
	f.methods["GetCookies"] = func(L *lua.LState) int {
		L.Push(lua.LString(strings.Join(h.Cookies.All(), "; ")))
		return 1
	}
	f.methods["AddServerCookies"] = func(L *lua.LState) int {
		h.Cookies.Add(L.CheckString(2))
		return 0
	}
	f.methods["SetProxy"] = func(L *lua.LState) int { return 0 } // proxying is host policy
	return f.push(L)
}
