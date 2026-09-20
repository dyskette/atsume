package download

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"time"
)

// Limiter gates outbound requests per host.
type Limiter interface {
	Wait(ctx context.Context, host string) error
}

// Fetcher downloads a chapter's images.
type Fetcher struct {
	Client  *http.Client
	Limiter Limiter
	// Referer is sent with every image request. Many hosts serve a placeholder
	// or a 403 without one, which is why modules set it in OnBeforeDownloadImage.
	Referer   string
	UserAgent string
	// MaxBytes caps a single image, guarding against a misparsed link pointing
	// at something enormous.
	MaxBytes int64
}

// NewFetcher returns a fetcher with sensible defaults. transport may be nil.
func NewFetcher(limiter Limiter, transport http.RoundTripper) *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: 120 * time.Second, Transport: transport},
		Limiter:   limiter,
		UserAgent: defaultUserAgent,
		MaxBytes:  64 << 20,
	}
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Progress is called after each page is fetched.
type Progress func(done, total int)

// Page downloads one image.
//
// Callers drive the page loop themselves because a module may implement
// OnDownloadImage and fetch some pages on its own. Ordering matters more than
// speed regardless: a CBZ is read page by page, and the per-host limiter would
// serialise concurrent fetches anyway.
func (f *Fetcher) Page(ctx context.Context, rawURL string, headers map[string]string) (Page, error) {
	return f.page(ctx, rawURL, headers)
}

func (f *Fetcher) page(ctx context.Context, rawURL string, headers map[string]string) (Page, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return Page{}, err
	}
	if f.Limiter != nil {
		if err := f.Limiter.Wait(ctx, req.URL.Host); err != nil {
			return Page{}, err
		}
	}
	req.Header.Set("User-Agent", f.UserAgent)
	if f.Referer != "" {
		req.Header.Set("Referer", f.Referer)
	}
	// Headers the module set in OnBeforeDownloadImage win over the defaults.
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := f.Client.Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("http %d", resp.StatusCode)
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, f.MaxBytes))
	if err != nil {
		return Page{}, err
	}
	if len(data) == 0 {
		return Page{}, fmt.Errorf("empty response")
	}
	return Page{Data: data, Ext: ImageExt(data, rawURL, resp.Header.Get("Content-Type"))}, nil
}

// ImageExt picks a file extension for an image.
//
// The bytes are consulted first because they are the only source that stays
// correct after a module transforms the image: descrambling a WebP re-encodes
// it as PNG, and trusting the URL there would name a PNG ".webp".
func ImageExt(data []byte, rawURL, contentType string) string {
	switch {
	case bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")):
		return ".png"
	case bytes.HasPrefix(data, []byte("\xff\xd8\xff")):
		return ".jpg"
	case bytes.HasPrefix(data, []byte("GIF8")):
		return ".gif"
	case len(data) > 12 && bytes.Equal(data[0:4], []byte("RIFF")) && bytes.Equal(data[8:12], []byte("WEBP")):
		return ".webp"
	}
	if ext := path.Ext(strings.SplitN(rawURL, "?", 2)[0]); isImageExt(ext) {
		return strings.ToLower(ext)
	}
	switch {
	case strings.Contains(contentType, "png"):
		return ".png"
	case strings.Contains(contentType, "webp"):
		return ".webp"
	case strings.Contains(contentType, "gif"):
		return ".gif"
	case strings.Contains(contentType, "avif"):
		return ".avif"
	default:
		return ".jpg"
	}
}

func isImageExt(ext string) bool {
	switch strings.ToLower(ext) {
	case ".jpg", ".jpeg", ".png", ".webp", ".gif", ".avif", ".bmp":
		return true
	}
	return false
}
