package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
	"github.com/dyskette/atsume/internal/web/ui"
)

func (s *Server) handleLibrary(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	series, err := s.App.Store.ListSeries(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}
	progress, err := s.App.Store.Progress(r.Context())
	if err != nil {
		s.fail(w, r, err)
		return
	}

	v := ui.LibraryView{Destination: s.App.Cfg.LibraryDir}
	for _, item := range series {
		v.Rows = append(v.Rows, ui.LibraryRow{Series: item, Progress: progress[item.ID]})
	}
	s.render(w, r, ui.Library(v))
}

func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	v := ui.BuildSites(s.App.ModuleCatalogue(r.Context()), query)

	// htmx sends this header when swapping the list in place; a full page load
	// needs the chrome around it.
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, r, ui.SiteGroups(v))
		return
	}
	s.render(w, r, ui.Sites(v))
}

// handleBrowse renders the page without touching the site.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, ui.Browse(ui.BrowseView{
		Module: r.PathValue("name"),
		Page:   queryPage(r),
	}))
}

// handleBrowseList fetches the index, which is what can be slow.
func (s *Server) handleBrowseList(w http.ResponseWriter, r *http.Request) {
	module, page := r.PathValue("name"), queryPage(r)

	entries, err := s.App.Browse(r.Context(), module, page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	tracked, err := s.App.Store.TrackedURLs(r.Context(), module)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.BrowseList(ui.BrowseView{
		Module: module, Page: page, Entries: entries, Tracked: tracked,
	}))
}

func queryPage(r *http.Request) int {
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 0 {
		return 0
	}
	return page
}

func (s *Server) handleTrackSeries(w http.ResponseWriter, r *http.Request) {
	module := r.FormValue("module")
	url := r.FormValue("url")
	if module == "" || url == "" {
		http.Error(w, "module and url are required", http.StatusBadRequest)
		return
	}
	if err := s.App.EnqueueRefresh(r.Context(), module, url); err != nil {
		s.fail(w, r, err)
		return
	}
	// The title, not the link: the confirmation replaces the row the reader
	// just pressed, and a URL slug there reads as a glitch.
	name := r.FormValue("name")
	if name == "" {
		name = url
	}
	s.render(w, r, ui.Tracked(name))
}

func (s *Server) handleSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	series, err := s.App.Store.GetSeries(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	chapters, err := s.App.Store.ListChapters(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.SeriesPage(s.seriesView(r.Context(), series, chapters)))
}

// seriesView assembles what the series page renders.
func (s *Server) seriesView(ctx context.Context, series store.Series, chapters []store.Chapter) ui.SeriesView {
	v := ui.SeriesView{
		Series:        series,
		Chapters:      chapters,
		Counts:        ui.CountChapters(chapters),
		Destination:   s.App.SeriesDestination(series.Title),
		CheckInterval: s.App.Cfg.CheckInterval,
	}
	if info, ok := s.App.SiteInfo(ctx, series.Key()); ok {
		v.SiteNeedsLogin = info.NeedsLogin
	}
	v.SiteHasCredentials = s.App.Store.HasCredentials(ctx, series.Key())
	return v
}

// handleCover serves a series cover through atsume rather than linking it.
func (s *Server) handleCover(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	data, contentType, err := s.App.Cover(r.Context(), id)
	if err != nil {
		// A missing cover is ordinary — plenty of sites do not publish one, and
		// some refuse the request. The page renders without it.
		slog.Debug("cover unavailable", "series", id, "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

func (s *Server) handleRefreshSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	series, err := s.App.Store.GetSeries(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.App.EnqueueRefresh(r.Context(), series.Key(), series.URL); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	n, err := s.App.EnqueueAllPending(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	slog.Info("queued chapters", "series", id, "count", n)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDownloadChapter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.App.EnqueueDownload(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	chapter, err := s.App.Store.GetChapter(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ChapterRow(chapter))
}

func (s *Server) handleModuleSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.App.ModuleSettings(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ModuleSettings(settings, false))
}

func (s *Server) handleSaveModuleSettings(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	settings, err := s.App.ModuleSettings(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Read every declared option rather than whatever the form posted, so an
	// unchecked checkbox — which browsers omit entirely — is recorded as off.
	values := map[string]string{}
	for _, o := range settings.Options {
		if o.Kind == scraper.OptionCheckBox {
			values[o.Name] = "0"
			if r.PostForm.Get(o.Name) != "" {
				values[o.Name] = "1"
			}
			continue
		}
		values[o.Name] = r.PostForm.Get(o.Name)
	}

	username := r.PostForm.Get("__username")
	password := r.PostForm.Get("__password")
	updateLogin := settings.SupportsLogin && settings.SecretsEnabled
	if updateLogin && password == "" && settings.HasCredentials && username != "" {
		// A blank password with a username means "keep the stored one".
		updateLogin = false
	}

	if err := s.App.SaveModuleSettings(r.Context(), name, values, username, password, updateLogin); err != nil {
		s.fail(w, r, err)
		return
	}

	settings, err = s.App.ModuleSettings(r.Context(), name)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ModuleSettings(settings, true))
}

// handlePreview shows a series before it is followed.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	module := r.PathValue("name")
	seriesURL := r.URL.Query().Get("url")
	if seriesURL == "" {
		http.Error(w, "no series address given", http.StatusBadRequest)
		return
	}

	info, err := s.App.Preview(r.Context(), module, seriesURL)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.Preview(ui.PreviewView{
		Module:    module,
		SeriesURL: seriesURL,
		Info:      info,
		Chapters:  info.ChapterLinks.Count(),
	}))
}

// handlePreviewCover proxies a cover for a series with no library row to key
// a cache on.
func (s *Server) handlePreviewCover(w http.ResponseWriter, r *http.Request) {
	data, contentType, err := s.App.CoverByURL(r.Context(), r.PathValue("name"), r.URL.Query().Get("url"))
	if err != nil {
		slog.Debug("preview cover unavailable", "err", err)
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	_, _ = w.Write(data)
}

// handleSubscribe turns automatic checking for one series on or off.
func (s *Server) handleSubscribe(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	on := r.FormValue("on") == "1"
	if err := s.App.Store.SetSubscribed(r.Context(), id, on); err != nil {
		s.fail(w, r, err)
		return
	}
	series, err := s.App.Store.GetSeries(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	chapters, err := s.App.Store.ListChapters(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.FollowControl(s.seriesView(r.Context(), series, chapters)))
}

// handleCheckNow asks the scheduler for an immediate sweep.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	s.App.Scheduler.CheckNow()
	w.WriteHeader(http.StatusNoContent)
}

// handleQueue serves the status line, which the footer fetches on load.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	q, err := s.App.Store.Queue(r.Context())
	if err != nil {
		slog.Error("queue status", "err", err)
	}
	s.render(w, r, ui.QueueStatus(q))
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	stats, err := s.App.Queue.Stats(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"modules":%d,"ref":%q,"check_interval":%q,"jobs":{"pending":%d,"running":%d,"failed":%d}}`,
		len(s.App.Registry.Modules()), s.App.Registry.Ref(),
		s.App.Cfg.CheckInterval.String(),
		stats["pending"], stats["running"], stats["failed"])
}
