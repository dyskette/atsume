package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Flaresolverr solves anti-bot challenges through a FlareSolverr instance.
//
// It is only consulted after a request has already been refused, so an
// unprotected site never pays for it.
type Flaresolverr struct {
	// URL is the FlareSolverr base address, e.g. http://flaresolverr:8191.
	URL    string
	Client *http.Client
}

// NewFlaresolverr returns a solver, disabled when no URL is configured.
func NewFlaresolverr(url string) *Flaresolverr {
	if url == "" {
		return nil
	}
	return &Flaresolverr{
		URL: strings.TrimSuffix(url, "/"),
		// Solving runs a real browser, so the timeout is generous.
		Client: &http.Client{Timeout: 120 * time.Second},
	}
}

type flareRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int    `json:"maxTimeout"`
}

type flareResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		URL       string `json:"url"`
		Status    int    `json:"status"`
		Response  string `json:"response"`
		UserAgent string `json:"userAgent"`
		Cookies   []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"cookies"`
	} `json:"solution"`
}

// Solution is what a solved challenge yields.
type Solution struct {
	Body      []byte
	Status    int
	UserAgent string
	Cookies   []string // as name=value
}

// Solve fetches a URL through FlareSolverr.
func (f *Flaresolverr) Solve(ctx context.Context, rawURL string) (*Solution, error) {
	body, err := json.Marshal(flareRequest{Cmd: "request.get", URL: rawURL, MaxTimeout: 60000})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, f.URL+"/v1", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := f.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("flaresolverr unreachable: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out flareResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("flaresolverr returned unparseable JSON: %w", err)
	}
	if out.Status != "ok" {
		return nil, fmt.Errorf("flaresolverr: %s", out.Message)
	}

	sol := &Solution{
		Body:      []byte(out.Solution.Response),
		Status:    out.Solution.Status,
		UserAgent: out.Solution.UserAgent,
	}
	for _, c := range out.Solution.Cookies {
		sol.Cookies = append(sol.Cookies, c.Name+"="+c.Value)
	}
	return sol, nil
}

// challengeMarkers are the strings an interstitial serves. A refusal alone is
// not enough: plenty of 403s are ordinary and would waste a browser run.
var challengeMarkers = []string{
	"Just a moment",
	"cf-browser-verification",
	"cf_chl_opt",
	"_cf_chl_",
	"Checking your browser",
	"DDoS-Guard",
	"__ddg",
}

// looksLikeChallenge reports whether a response is an anti-bot interstitial.
func looksLikeChallenge(status int, body []byte) bool {
	if status != http.StatusForbidden && status != http.StatusServiceUnavailable {
		return false
	}
	// Only the head of the document is examined; these markers appear early and
	// a chapter page can be a megabyte.
	head := body
	if len(head) > 8192 {
		head = head[:8192]
	}
	for _, m := range challengeMarkers {
		if bytes.Contains(head, []byte(m)) {
			return true
		}
	}
	return false
}

// trySolve attempts to recover a refused response through FlareSolverr.
func (h *HTTP) trySolve(rawURL string) bool {
	if h.solver == nil || !looksLikeChallenge(h.ResultCode, h.Document.Bytes()) {
		return false
	}
	slog.Info("anti-bot challenge; solving", "url", rawURL)

	sol, err := h.solver.Solve(h.ctx, rawURL)
	if err != nil {
		slog.Warn("flaresolverr failed", "url", rawURL, "err", err)
		return false
	}

	h.Document.Set(sol.Body)
	h.ResultCode = sol.Status
	if sol.UserAgent != "" {
		// The cookies are bound to the agent that earned them, so both have to
		// be carried forward or the next request is challenged again.
		h.UserAgent = sol.UserAgent
	}
	for _, c := range sol.Cookies {
		if name, value, ok := strings.Cut(c, "="); ok {
			h.Cookies.SetValue(name, value)
		}
	}
	return sol.Status >= 200 && sol.Status < 400
}
