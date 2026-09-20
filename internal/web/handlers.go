package web

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyskette/atsume/internal/scraper"
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
	s.render(w, r, ui.Library(series))
}

func (s *Server) handleModules(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	mods := filterModules(s.App.Registry.Modules(), query)

	// htmx sends this header when swapping the list in place; a full page load
	// needs the chrome around it.
	if r.Header.Get("HX-Request") == "true" {
		s.render(w, r, ui.ModuleItems(mods))
		return
	}
	s.render(w, r, ui.ModuleList(mods, s.App.Registry.Ref(), query))
}

func filterModules(mods []scraper.ModuleInfo, query string) []scraper.ModuleInfo {
	if query == "" {
		return mods
	}
	q := strings.ToLower(query)
	out := make([]scraper.ModuleInfo, 0, len(mods))
	for _, m := range mods {
		if strings.Contains(strings.ToLower(m.Name), q) {
			out = append(out, m)
		}
	}
	return out
}

func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	module := r.PathValue("name")
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 0 {
		page = 0
	}

	entries, err := s.App.Browse(r.Context(), module, page)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.Browse(module, page, entries))
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
	s.render(w, r, ui.Tracked(url))
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
	s.render(w, r, ui.SeriesPage(series, chapters))
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
	if err := s.App.EnqueueRefresh(r.Context(), series.ModuleName, series.URL); err != nil {
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
	s.render(w, r, ui.SubscribeButton(series))
}

// handleCheckNow asks the scheduler for an immediate sweep.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	s.App.Scheduler.CheckNow()
	w.WriteHeader(http.StatusNoContent)
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
