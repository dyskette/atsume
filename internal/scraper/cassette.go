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

	if !c.record {
		return c.replay(k, req)
	}
	return c.recordOne(k, req, body)
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
