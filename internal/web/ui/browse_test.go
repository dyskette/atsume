package ui

import (
	"testing"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/store"
)

// TestSiteStatus covers the line under a site's name in each state.
func TestSiteStatus(t *testing.T) {
	read := store.SiteCatalogue{Exists: true, Complete: true, Titles: 16, Source: store.SourceSite,
		BuiltAt: time.Now().Add(-2 * time.Hour), Resume: store.NoPos}
	shared := read
	shared.Source, shared.DataAt = store.SourcePrebuilt, time.Date(2026, 5, 3, 0, 0, 0, 0, time.UTC)
	partial := read
	partial.Complete, partial.Problem, partial.Status, partial.Steps, partial.Pages = false, app.ProblemBlocked, 403, 2, 12
	failed := store.SiteCatalogue{Exists: true, Problem: app.ProblemDown, Status: 502, BuiltAt: time.Now(), Resume: store.NoPos}

	cases := []struct {
		name, want string
		v          BrowseView
	}{
		{"not read", "Not read yet", BrowseView{}},
		{"first read", "Reading the site · page 4 · 87 titles so far",
			BrowseView{Indexing: true, Progress: app.ReadProgress{Page: 4, Titles: 87}}},
		{"refresh", "Refreshing · page 2 of about 3",
			BrowseView{Indexing: true, Catalogue: store.SiteCatalogue{OverList: true}, Progress: app.ReadProgress{Page: 2, Estimate: 3}}},
		{"read", "16 titles · read 2h ago", BrowseView{Catalogue: read}},
		{"shared", "16 titles · updated May 2026", BrowseView{Catalogue: shared}},
		{"partial", "16 titles · read incomplete · 2h ago", BrowseView{Catalogue: partial}},
		{"failed, nothing kept", "Last read failed · just now", BrowseView{Catalogue: failed}},
	}
	for _, c := range cases {
		if got := c.v.Status(); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}

	v := BrowseView{Module: "Asura Scans", Catalogue: partial}
	if got := v.StoppedAt() + ": " + v.Reason(); got != "page 3 of about 12: Asura Scans blocked the request (HTTP 403)" {
		t.Errorf("stopped at: %q", got)
	}
	if !v.Incomplete() || !v.Failed() {
		t.Error("a partial blocked read should be incomplete and failed")
	}
}

// TestSiteRetry covers what the page says atsume will do by itself.
func TestSiteRetry(t *testing.T) {
	down := store.SiteCatalogue{Problem: app.ProblemDown, Retries: 1, RetryAt: time.Now().Add(29*time.Minute + 50*time.Second)}
	if got := (BrowseView{Catalogue: down}).Retry(); got != "atsume will try again automatically in 30 min." {
		t.Errorf("scheduled: %q", got)
	}
	down.RetryAt, down.Retries = time.Time{}, 3
	if got := (BrowseView{Catalogue: down}).Retry(); got != "atsume tried 4 times and stopped; try again when the site is back." {
		t.Errorf("given up: %q", got)
	}
	if got := (BrowseView{Catalogue: store.SiteCatalogue{Problem: app.ProblemBlocked}}).Retry(); got != "" {
		t.Errorf("a blocked site is not retried, got %q", got)
	}
}

// TestSiteNoMatchOffersRefresh covers when a search with no match suggests
// reading the site again.
func TestSiteNoMatchOffersRefresh(t *testing.T) {
	fresh := BrowseView{Catalogue: store.SiteCatalogue{Exists: true, Titles: 5, Source: store.SourceSite, BuiltAt: time.Now()}}
	old := fresh
	old.Catalogue.BuiltAt = time.Now().Add(-48 * time.Hour)
	shared := fresh
	shared.Catalogue.Source = store.SourcePrebuilt
	if fresh.OfferRefresh() || !old.OfferRefresh() || !shared.OfferRefresh() {
		t.Errorf("offer refresh: fresh=%v old=%v shared=%v", fresh.OfferRefresh(), old.OfferRefresh(), shared.OfferRefresh())
	}
	reading := old
	reading.Indexing = true
	if reading.OfferRefresh() {
		t.Error("a refresh should not be offered while one runs")
	}
}
