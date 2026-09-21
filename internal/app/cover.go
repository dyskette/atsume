package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/dyskette/atsume/internal/download"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
)

// Cover fetches a series' cover image, caching it on disk.
//
// It is proxied rather than linked directly from the page for two reasons: the
// image hosts these sites use routinely refuse a request whose Referer is not
// their own, and a direct link would have every page view hit someone else's
// server. The module's root URL is the Referer the site expects, which is the
// same thing OnBeforeDownloadImage sets for page images.
func (a *App) Cover(ctx context.Context, seriesID int64) (data []byte, contentType string, err error) {
	series, err := a.Store.GetSeries(ctx, seriesID)
	if err != nil {
		return nil, "", err
	}
	if series.CoverURL == "" {
		return nil, "", fmt.Errorf("series %d has no cover", seriesID)
	}

	cacheDir := filepath.Join(a.Cfg.DataDir, "covers")
	cached := filepath.Join(cacheDir, fmt.Sprintf("%d", seriesID))
	if b, err := os.ReadFile(cached); err == nil && len(b) > 0 {
		return b, http.DetectContentType(b), nil
	}

	data, err = a.fetchCover(ctx, series.Key(), series.CoverURL)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err == nil {
		_ = os.WriteFile(cached, data, 0o644)
	}
	return data, http.DetectContentType(data), nil
}

// CoverByURL fetches a cover for a series that is not in the library yet,
// which is the preview's case: there is no row to key a cache on.
//
// The URL comes from the module's own output rather than from the reader, and
// the endpoint sits behind whatever guards the rest of the interface, but the
// scheme is still checked so a malformed module cannot turn this into a
// general-purpose fetcher.
func (a *App) CoverByURL(ctx context.Context, moduleName, coverURL string) (data []byte, contentType string, err error) {
	u, err := url.Parse(coverURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, "", fmt.Errorf("not a fetchable cover address")
	}

	sum := sha256.Sum256([]byte(coverURL))
	cacheDir := filepath.Join(a.Cfg.DataDir, "covers")
	cached := filepath.Join(cacheDir, "u"+hex.EncodeToString(sum[:])[:20])
	if b, readErr := os.ReadFile(cached); readErr == nil && len(b) > 0 {
		return b, http.DetectContentType(b), nil
	}

	data, err = a.fetchCover(ctx, moduleName, coverURL)
	if err != nil {
		return nil, "", err
	}
	if err := os.MkdirAll(cacheDir, 0o755); err == nil {
		_ = os.WriteFile(cached, data, 0o644)
	}
	return data, http.DetectContentType(data), nil
}

// fetchCover retrieves and downscales one cover.
func (a *App) fetchCover(ctx context.Context, moduleName, coverURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, coverURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", scraperUserAgent)
	if root := a.moduleRootURL(ctx, moduleName); root != "" {
		req.Header.Set("Referer", root)
	}

	client := &http.Client{Timeout: 20 * time.Second, Transport: a.Transport}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("cover request returned %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	return thumbnail(raw), nil
}

// coverWidth is what a cover is cached at.
//
// Sites publish covers at full page size — commonly several hundred kilobytes
// for something displayed a couple of hundred pixels wide. A library of fifty
// series would otherwise pull tens of megabytes to draw a grid of thumbnails.
const coverWidth = 400

// thumbnail scales a cover down, returning the original when it cannot.
//
// Failing to resize is not worth failing the request over: an oversized cover
// still renders, and some formats simply will not decode here.
func thumbnail(raw []byte) []byte {
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	b := src.Bounds()
	if b.Dx() <= coverWidth {
		return raw
	}

	height := b.Dy() * coverWidth / b.Dx()
	dst := image.NewRGBA(image.Rect(0, 0, coverWidth, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, b, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 82}); err != nil {
		return raw
	}
	return buf.Bytes()
}

// moduleRootURL reads a module's root address, or "" when it cannot be loaded.
func (a *App) moduleRootURL(ctx context.Context, name string) string {
	r, err := a.openModuleRaw(ctx, name)
	if err != nil {
		return ""
	}
	defer r.Close()
	return r.Module().RootURL
}

// scraperUserAgent matches what the scraper sends, since a cover host that
// filters on it would refuse anything else.
const scraperUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36"

// SeriesDestination is the directory this series' chapters are written to.
//
// It is shown on the page because it is the only visible connection between
// atsume and whatever library server reads the files.
func (a *App) SeriesDestination(title string) string {
	return filepath.Join(a.Cfg.LibraryDir, download.Sanitize(title))
}
