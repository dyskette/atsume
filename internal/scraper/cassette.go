package scraper

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

// Cassette records HTTP exchanges to disk and replays them later.
//
// It exists so the scraper can be tested against real site markup without
// reaching the network on every run. Recorded pages are the only fixtures that
// carry the things hand-written HTML never does: unclosed tags, entity
// encodings, absolute and relative URLs mixed in one document, and whitespace
// exactly where a selector trips over it.
type Cassette struct {
	dir string
	// record performs the request for real and saves the result. Replay is the
	// default so that a normal test run is hermetic.
	record bool
	next   http.RoundTripper

	mu   sync.Mutex
	used map[string]bool
	// matchers are seeded responses selected by a substring of the request
	// body. They exist because an API can serve several operations from one
	// URL — a GraphQL endpoint being the usual case — so neither the URL nor an
	// exact body is a workable key for a hand-written fixture.
	matchers []matcher
}

type matcher struct {
	method   string
	url      string
	contains string
	status   int
	body     []byte
}

// NewCassette opens a cassette directory. In record mode the directory is
// created and requests pass through to next, which may be nil for the default
// transport.
func NewCassette(dir string, record bool, next http.RoundTripper) (*Cassette, error) {
	if record {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	if next == nil {
		next = http.DefaultTransport
	}
	return &Cassette{dir: dir, record: record, next: next, used: map[string]bool{}}, nil
}

// exchange is one recorded request and response.
type exchange struct {
	Method string      `json:"method"`
	URL    string      `json:"url"`
	Status int         `json:"status"`
	Header http.Header `json:"header,omitempty"`
	// Body is stored as text when it is valid UTF-8, so a recorded page stays
	// readable and reviewable in a diff.
	Body   string `json:"body,omitempty"`
	Base64 string `json:"body_base64,omitempty"`
}

// key identifies an exchange. The request body is included because a module
// pages through a listing by POSTing different form values to one URL.
func key(method, url string, body []byte) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n%s\n", method, url)
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// urlKey identifies an exchange by method and URL alone.
//
// It is the fallback when no body-specific recording exists, which covers the
// common case of a POST carrying a nonce or timestamp that differs on every
// run and would otherwise never match a recording.
func urlKey(method, url string) string {
	h := sha256.New()
	fmt.Fprintf(h, "url-only\n%s\n%s\n", method, url)
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// PutMatching seeds a response selected by a substring of the request body.
func (c *Cassette) PutMatching(method, url, contains string, status int, body []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.matchers = append(c.matchers, matcher{
		method: method, url: url, contains: contains, status: status, body: body,
	})
}

// match finds a seeded matcher for a request.
func (c *Cassette) match(method, url string, body []byte) (matcher, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, m := range c.matchers {
		if m.method == method && m.url == url && strings.Contains(string(body), m.contains) {
			return m, true
		}
	}
	return matcher{}, false
}

// Put seeds a response into the cassette.
//
// A nil reqBody stores it under the URL-only key, so a hand-written fixture
// need not reproduce a request body exactly.
func (c *Cassette) Put(method, url string, reqBody []byte, status int, respBody []byte) error {
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		return err
	}
	k := urlKey(method, url)
	if reqBody != nil {
		k = key(method, url, reqBody)
	}
	ex := exchange{Method: method, URL: url, Status: status}
	if isText(respBody) {
		ex.Body = string(respBody)
	} else {
		ex.Base64 = encodeBase64(respBody)
	}
	out, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(c.path(k), out, 0o644)
}

func (c *Cassette) path(k string) string { return filepath.Join(c.dir, k+".json") }

// RoundTrip replays a recorded response, or records a live one.
func (c *Cassette) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		req.Body.Close()
		if err != nil {
			return nil, err
		}
		req.Body = io.NopCloser(bytes.NewReader(body))
	}

	k := key(req.Method, req.URL.String(), body)
	c.mu.Lock()
	c.used[k] = true
	c.mu.Unlock()

	if c.record {
		return c.recordOne(k, req, body)
	}
	if m, ok := c.match(req.Method, req.URL.String(), body); ok {
		return &http.Response{
			StatusCode:    m.status,
			Status:        http.StatusText(m.status),
			Header:        http.Header{},
			Body:          io.NopCloser(bytes.NewReader(m.body)),
			ContentLength: int64(len(m.body)),
			Request:       req,
		}, nil
	}
	// Fall back to a URL-only recording when nothing matches the exact body.
	if _, err := os.Stat(c.path(k)); err != nil {
		if alt := urlKey(req.Method, req.URL.String()); alt != k {
			if _, err := os.Stat(c.path(alt)); err == nil {
				c.mu.Lock()
				c.used[alt] = true
				c.mu.Unlock()
				return c.replay(alt, req)
			}
		}
	}
	return c.replay(k, req)
}

func (c *Cassette) replay(k string, req *http.Request) (*http.Response, error) {
	raw, err := os.ReadFile(c.path(k))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("no recording for %s %s in %s; re-record with ATSUME_RECORD=1",
				req.Method, req.URL, c.dir)
		}
		return nil, err
	}
	var ex exchange
	if err := json.Unmarshal(raw, &ex); err != nil {
		return nil, fmt.Errorf("%s: %w", c.path(k), err)
	}

	data := []byte(ex.Body)
	if ex.Base64 != "" {
		if data, err = decodeBase64(ex.Base64); err != nil {
			return nil, err
		}
	}
	header := ex.Header
	if header == nil {
		header = http.Header{}
	}
	return &http.Response{
		StatusCode:    ex.Status,
		Status:        http.StatusText(ex.Status),
		Header:        header,
		Body:          io.NopCloser(bytes.NewReader(data)),
		ContentLength: int64(len(data)),
		Request:       req,
	}, nil
}

func (c *Cassette) recordOne(k string, req *http.Request, reqBody []byte) (*http.Response, error) {
	resp, err := c.next.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return nil, err
	}

	ex := exchange{
		Method: req.Method,
		URL:    req.URL.String(),
		Status: resp.StatusCode,
		Header: pruneHeaders(resp.Header),
	}
	if isText(data) {
		ex.Body = string(data)
	} else {
		ex.Base64 = encodeBase64(data)
	}

	out, err := json.MarshalIndent(ex, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(c.path(k), out, 0o644); err != nil {
		return nil, err
	}

	resp.Body = io.NopCloser(bytes.NewReader(data))
	return resp, nil
}

// pruneHeaders drops the response headers that change between runs, so a
// re-recording produces a reviewable diff rather than noise.
func pruneHeaders(h http.Header) http.Header {
	drop := map[string]bool{
		"Date": true, "Set-Cookie": true, "Expires": true, "Age": true,
		"Cf-Ray": true, "Report-To": true, "Nel": true, "Alt-Svc": true,
		"Server-Timing": true, "X-Request-Id": true, "Etag": true,
	}
	out := http.Header{}
	var names []string
	for name := range h {
		if !drop[http.CanonicalHeaderKey(name)] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		out[http.CanonicalHeaderKey(name)] = h[name]
	}
	return out
}

// isText reports whether data can be stored as readable text.
func isText(data []byte) bool {
	if !utf8Valid(data) {
		return false
	}
	// A NUL byte means binary even when the bytes happen to be valid UTF-8.
	return !bytes.ContainsRune(data, 0)
}

// Unused returns the recordings in the directory that this cassette never
// replayed, which is how a test spots fixtures left behind by a module change.
func (c *Cassette) Unused() ([]string, error) {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	var out []string
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if filepath.Ext(e.Name()) == ".json" && !c.used[name] {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

// utf8Valid, encodeBase64 and decodeBase64 are thin wrappers kept here so the
// cassette format stays described in one file.
func utf8Valid(b []byte) bool { return utf8.Valid(b) }

func encodeBase64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

func decodeBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }
