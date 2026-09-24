package web

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCrossOriginRequestsAreRefused covers the forgery protection on the
// router: a POST another site's page sends must not reach a handler.
func TestCrossOriginRequestsAreRefused(t *testing.T) {
	// No App: a request refused here never reaches a handler, and the ones
	// that pass go to routes that do not need one (a static file, or a path
	// only GET serves).
	h := (&Server{}).routes()

	cases := []struct {
		name    string
		method  string
		path    string
		headers map[string]string
		refused bool
	}{
		{"cross-site form post", "POST", "/check", map[string]string{"Sec-Fetch-Site": "cross-site"}, true},
		{"same-site but other origin", "POST", "/check", map[string]string{"Sec-Fetch-Site": "same-site"}, true},
		{"older browser, foreign Origin", "POST", "/download", map[string]string{"Origin": "https://evil.example"}, true},
		{"the page itself", "POST", "/nope", map[string]string{"Sec-Fetch-Site": "same-origin"}, false},
		{"older browser, own Origin", "POST", "/nope", map[string]string{"Origin": "http://atsume.test"}, false},
		{"typed into the address bar", "POST", "/nope", map[string]string{"Sec-Fetch-Site": "none"}, false},
		{"not a browser", "POST", "/nope", nil, false},
		{"a cross-site GET is safe", "GET", "/static/app.css", map[string]string{"Sec-Fetch-Site": "cross-site"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			req := httptest.NewRequest(c.method, "http://atsume.test"+c.path, nil)
			for k, v := range c.headers {
				req.Header.Set(k, v)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if refused := rec.Code == http.StatusForbidden; refused != c.refused {
				t.Errorf("status %d, want refused=%v", rec.Code, c.refused)
			}
		})
	}
}
