package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

// Notification is the payload posted when a check finds new chapters.
//
// The shape is deliberately plain JSON rather than any one service's format:
// ntfy, Gotify, Apprise and a Discord webhook all differ, and guessing which
// from the URL would be a heuristic that breaks on self-hosted instances. A
// bridge or a service that accepts arbitrary JSON handles this directly.
type Notification struct {
	Event    string   `json:"event"`
	Series   string   `json:"series"`
	SeriesID int64    `json:"series_id"`
	Module   string   `json:"module"`
	Count    int      `json:"count"`
	Chapters []string `json:"chapters"`
	Message  string   `json:"message"`
}

// Notifier posts notifications to a configured URL.
type Notifier struct {
	URL    string
	Client *http.Client
}

// NewNotifier returns a notifier, disabled when no URL is configured.
func NewNotifier(url string, transport http.RoundTripper) *Notifier {
	return &Notifier{
		URL:    url,
		Client: &http.Client{Timeout: 15 * time.Second, Transport: transport},
	}
}

// Enabled reports whether notifications are configured.
func (n *Notifier) Enabled() bool { return n != nil && n.URL != "" }

// Notify posts a notification.
//
// A failure is logged and swallowed: a notification endpoint being down is not
// a reason to fail the download that triggered it.
func (n *Notifier) Notify(ctx context.Context, msg Notification) {
	if !n.Enabled() {
		return
	}
	body, err := json.Marshal(msg)
	if err != nil {
		slog.Error("notify: encode", "err", err)
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.URL, bytes.NewReader(body))
	if err != nil {
		slog.Error("notify: request", "err", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := n.Client.Do(req)
	if err != nil {
		slog.Warn("notify: post failed", "url", n.URL, "err", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		slog.Warn("notify: rejected", "url", n.URL, "status", resp.StatusCode)
	}
}

// newChaptersMessage builds the notification for a check that found chapters.
func newChaptersMessage(seriesID int64, series, module string, names []string) Notification {
	return Notification{
		Event:    "new_chapters",
		Series:   series,
		SeriesID: seriesID,
		Module:   module,
		Count:    len(names),
		Chapters: names,
		Message:  fmt.Sprintf("%s: %d new chapter(s)", series, len(names)),
	}
}
