package download

import (
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

// NewFetcher returns a fetcher with sensible defaults.
func NewFetcher(limiter Limiter) *Fetcher {
	return &Fetcher{
		Client:    &http.Client{Timeout: 120 * time.Second},
		Limiter:   limiter,
		UserAgent: defaultUserAgent,
		MaxBytes:  64 << 20,
	}
}

const defaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// Progress is called after each page is fetched.
type Progress func(done, total int)

// Pages downloads every URL in order.
//
// Ordering matters more than speed here: a CBZ is read page by page, so pages
// are fetched sequentially rather than racing and reassembling. The per-host
// limiter would serialise them anyway.
func (f *Fetcher) Pages(ctx context.Context, urls []string, onProgress Progress) ([]Page, error) {
	pages := make([]Page, 0, len(urls))
	for i, u := range urls {
		p, err := f.page(ctx, u)
		if err != nil {
			return nil, fmt.Errorf("page %d of %d (%s): %w", i+1, len(urls), u, err)
		}
		pages = append(pages, p)
		if onProgress != nil {
			onProgress(i+1, len(urls))
		}
	}
	return pages, nil
}

func (f *Fetcher) page(ctx context.Context, rawURL string) (Page, error) {
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
	return Page{Data: data, Ext: imageExt(rawURL, resp.Header.Get("Content-Type"))}, nil
}

// imageExt picks a file extension, preferring the URL and falling back to the
// declared content type.
func imageExt(rawURL, contentType string) string {
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
