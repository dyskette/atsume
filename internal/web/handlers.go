package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/jobs"
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

	v := ui.LibraryView{
		Destination:   s.App.Cfg.LibraryDir,
		CheckInterval: s.App.Cfg.CheckInterval,
		Uptime:        s.App.Uptime(),
	}
	v.LastSweep, v.Swept = s.App.Scheduler.LastSweep()
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

// browseRows is how many titles one screenful holds. A catalogue can run to
// thousands, and shipping all of them so a script can hide most is how a
// page becomes unusable on the device most likely to be reading it.
const browseRows = 120

// handleBrowse renders the page from what is stored, which is instant.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	v, err := s.browseView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// A site nobody has read yet is read now: the reader opened it to see
	// titles, and asking them to press a second button to get what they
	// came for is a toll, not a choice.
	if v.Empty() {
		// Whichever is quicker: a published snapshot if there is one, the
		// site itself otherwise.
		if err := s.App.EnqueueIndex(r.Context(), v.Module, ""); err != nil {
			s.fail(w, r, err)
			return
		}
		v.Indexing = true
	}
	s.render(w, r, ui.Browse(v))
}

// handleBrowseList renders a slice of the stored catalogue.
func (s *Server) handleBrowseList(w http.ResponseWriter, r *http.Request) {
	v, err := s.browseView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// "Load more" appends to the list already on screen rather than
	// replacing it; a search replaces it.
	if r.URL.Query().Get("rows") != "" {
		s.render(w, r, ui.BrowseRows(v))
		return
	}
	s.render(w, r, ui.BrowseList(v))
}

// handleIndexSite reads a site's catalogue again.
func (s *Server) handleIndexSite(w http.ResponseWriter, r *http.Request) {
	// An explicit re-read means the site. Someone looking at a snapshot from
	// 2024 and pressing this wants what the site says now.
	if err := s.App.EnqueueIndex(r.Context(), r.PathValue("name"), store.SourceSite); err != nil {
		s.fail(w, r, err)
		return
	}
	s.catalogueStatus(w, r, false)
}

// handleRereadSite asks before spending minutes of a site's bandwidth.
func (s *Server) handleRereadSite(w http.ResponseWriter, r *http.Request) {
	v, err := s.browseView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ConfirmReread(v))
}

// handleCatalogueStatus re-renders the line that says where the list came
// from, which is also how the confirmation is dismissed.
func (s *Server) handleCatalogueStatus(w http.ResponseWriter, r *http.Request) {
	s.catalogueStatus(w, r, false)
}

func (s *Server) catalogueStatus(w http.ResponseWriter, r *http.Request, indexing bool) {
	v, err := s.browseView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	v.Indexing = v.Indexing || indexing
	s.render(w, r, ui.CatalogueStatus(v))
}

// browseView assembles a site's page from what is stored.
func (s *Server) browseView(r *http.Request) (ui.BrowseView, error) {
	module := s.App.ResolveModule(r.Context(), r.PathValue("name"))
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}

	v := ui.BrowseView{Module: module, Query: query, Offset: offset, Limit: browseRows}
	var err error
	if v.Catalogue, err = s.App.Store.SiteCatalogueInfo(r.Context(), module); err != nil {
		return v, err
	}
	v.Indexing = s.App.Indexing(r.Context(), module)
	if v.Titles, v.Found, err = s.App.Store.SearchSiteTitles(
		r.Context(), module, query, offset, browseRows); err != nil {
		return v, err
	}
	if v.Tracked, err = s.App.Store.TrackedURLs(r.Context(), module); err != nil {
		return v, err
	}
	return v, nil
}

// handleFollowMany follows several titles at once.
//
// Recognising six series in an index and following them one at a time is six
// round trips for one intent.
func (s *Server) handleFollowMany(w http.ResponseWriter, r *http.Request) {
	module := r.PathValue("name")
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	selected := r.PostForm["sel"]
	if len(selected) == 0 {
		s.render(w, r, ui.Notice("Nothing selected.", ""))
		return
	}

	var followed int
	for _, key := range selected {
		url := r.PostForm.Get("u" + key)
		if url == "" {
			continue
		}
		if _, err := s.App.Follow(r.Context(), module, url, r.PostForm.Get("t"+key)); err != nil {
			s.fail(w, r, err)
			return
		}
		followed++
	}

	slog.Info("followed several", "module", module, "count", followed)
	// Reloading the index would cost another request to the site, so the page
	// says what happened and offers the library rather than re-fetching.
	w.Header().Set("HX-Reswap", "outerHTML")
	w.Header().Set("HX-Retarget", "#follow-many")
	detail := "their chapter lists are being fetched"
	if followed == 1 {
		detail = "its chapter list is being fetched"
	}
	s.render(w, r, ui.Notice(
		fmt.Sprintf("Following %d more — %s.", followed, detail),
		"/"))
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
	name := r.FormValue("name")
	if name == "" {
		name = url
	}

	id, err := s.App.Follow(r.Context(), module, url, name)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The reply replaces whatever was pressed, in place.
	s.render(w, r, ui.Followed(name, id))
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
	v := s.seriesView(r.Context(), series, chapters)
	listOptions(r, &v)
	s.render(w, r, ui.SeriesPage(v))
}

// listOptions reads how the chapter list is shown from the address: which
// chapters, in which order, and whether all of them.
func listOptions(r *http.Request, v *ui.SeriesView) {
	q := r.URL.Query()
	switch f := q.Get("filter"); f {
	case ui.FilterDownloaded, ui.FilterNotDownloaded:
		v.Filter = f
	}
	v.Oldest = q.Get("sort") == "oldest"
	v.ShowAll = q.Get("all") == "1"
}

// rowStates gives every chapter its place in line or download progress.
func (s *Server) rowStates(ctx context.Context) map[int64]ui.RowState {
	out := map[int64]ui.RowState{}
	if positions, err := s.App.QueuePositions(ctx); err == nil {
		for id, p := range positions {
			out[id] = ui.RowState{Position: p}
		}
	}
	for id, p := range s.App.ActiveDownloads() {
		out[id] = ui.RowState{Active: true, Done: p.Done, Total: p.Total}
	}
	return out
}

// rowState is one chapter's place in line or download progress.
func (s *Server) rowState(ctx context.Context, c store.Chapter) ui.RowState {
	switch c.State {
	case store.ChapterDownloading:
		if p, ok := s.App.ActiveDownloads()[c.ID]; ok {
			return ui.RowState{Active: true, Done: p.Done, Total: p.Total}
		}
	case store.ChapterQueued:
		if positions, err := s.App.QueuePositions(ctx); err == nil {
			return ui.RowState{Position: positions[c.ID]}
		}
	}
	return ui.RowState{}
}

// seriesView assembles what the series page renders.
func (s *Server) seriesView(ctx context.Context, series store.Series, chapters []store.Chapter) ui.SeriesView {
	v := ui.SeriesView{
		Series:        series,
		Chapters:      chapters,
		Counts:        ui.CountChapters(chapters),
		Destination:   s.App.SeriesDestination(series.Title),
		CheckInterval: s.App.Cfg.CheckInterval,
		Missing:       s.App.MissingFiles(chapters),
		SiteURL:       s.App.SiteLink(ctx, series.Key(), series.URL),
		Rows:          s.rowStates(ctx),
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
	s.renderSeriesActions(w, r, id)
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
	s.render(w, r, ui.ChapterRowBody(chapter, false, s.rowState(r.Context(), chapter)))
}

// handleCancelChapter takes a chapter out of the queue or stops its download.
// A running download resets the chapter as it returns, and the live update
// brings that row; the reply is the row as it stands now.
func (s *Server) handleCancelChapter(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.App.CancelChapter(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	chapter, err := s.App.Store.GetChapter(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ChapterRowBody(chapter, false, s.rowState(r.Context(), chapter)))
}

func (s *Server) handleModuleSettings(w http.ResponseWriter, r *http.Request) {
	settings, err := s.App.ModuleSettings(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.ModuleSettingsPage(settings))
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
	// The chosen address is not something the module declares, so it is not
	// in the option list; it is still stored the same way.
	if len(settings.Mirrors) > 1 {
		if v := r.PostForm.Get(app.MirrorOption); v != "" {
			values[app.MirrorOption] = v
		}
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

	// A series in the library has one page, however it is reached.
	tracked, err := s.App.Store.TrackedURLs(r.Context(), module)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if id, ok := tracked[seriesURL]; ok {
		http.Redirect(w, r, fmt.Sprintf("/series/%d", id), http.StatusSeeOther)
		return
	}

	info, err := s.App.Preview(r.Context(), module, seriesURL)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	links, names := info.ChapterLinks.All(), info.ChapterNames.All()
	listed := make([]ui.ListedChapter, 0, len(links))
	for i, link := range links {
		name := link
		if i < len(names) {
			name = names[i]
		}
		listed = append(listed, ui.ListedChapter{Name: name, URL: link})
	}
	// The page names the site as the module declares it, as it does for a
	// series in the library, rather than by its file name.
	siteName := module
	if e, ok := s.App.SiteInfo(r.Context(), module); ok {
		siteName = e.Site
	}
	v := ui.SeriesView{
		Series: store.Series{
			ModuleKey: module, ModuleName: siteName, URL: seriesURL,
			Title: info.Title, CoverURL: info.CoverLink, Authors: info.Authors,
			Artists: info.Artists, Genres: info.Genres, Status: info.Status, Summary: info.Summary,
		},
		Module:        module,
		SeriesURL:     seriesURL,
		Listed:        listed,
		Destination:   s.App.SeriesDestination(info.Title),
		SiteURL:       s.App.SiteLink(r.Context(), module, seriesURL),
		CheckInterval: s.App.Cfg.CheckInterval,
	}
	listOptions(r, &v)
	s.render(w, r, ui.SeriesPage(v))
}

// handleFollowFromPage follows a series from its page. The series and its
// chapters are stored before the reply, so the page it reloads into is
// complete.
func (s *Server) handleFollowFromPage(w http.ResponseWriter, r *http.Request) {
	id, err := s.App.SaveAndFollow(r.Context(), r.PathValue("name"), r.FormValue("url"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", fmt.Sprintf("/series/%d", id))
	w.WriteHeader(http.StatusNoContent)
}

// handleDownloadFromPage downloads one chapter, or every chapter, of a series
// not in the library, which saves it there without following it. The page
// reloads as the library series.
func (s *Server) handleDownloadFromPage(w http.ResponseWriter, r *http.Request) {
	module, seriesURL := r.PathValue("name"), r.FormValue("url")
	var id int64
	var err error
	if chapter := r.FormValue("chapter"); chapter != "" {
		id, err = s.App.DownloadChapterOf(r.Context(), module, seriesURL, chapter)
	} else {
		id, err = s.App.SaveAndDownloadAll(r.Context(), module, seriesURL)
	}
	if err != nil {
		s.fail(w, r, err)
		return
	}
	w.Header().Set("HX-Redirect", fmt.Sprintf("/series/%d", id))
	w.WriteHeader(http.StatusNoContent)
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

// handleTestLogin signs in and says what happened.
//
// It tests what is in the form rather than what is in the database, so a
// password can be checked before it is committed to either.
func (s *Server) handleTestLogin(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ok, detail := s.App.TestLogin(r.Context(), r.PathValue("name"),
		r.PostForm.Get("__username"), r.PostForm.Get("__password"))
	s.render(w, r, ui.LoginResult(ok, detail))
}

// handleRecheck queues a fresh check of every series from one site.
func (s *Server) handleRecheck(w http.ResponseWriter, r *http.Request) {
	n, err := s.App.RecheckSite(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, ui.Notice("Queued "+ui.Count(n, "re-check", "re-checks")+".", "/"))
}

// handleDownloadMany queues the outstanding chapters of several series.
//
// Following in bulk without downloading in bulk was half a workflow: six
// titles followed from one index landed in a library that could only be
// acted on one page at a time.
func (s *Server) handleDownloadMany(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selected := r.PostForm["sel"]
	if len(selected) == 0 {
		s.render(w, r, ui.Notice("Nothing selected.", ""))
		return
	}

	var chapters, series int
	for _, raw := range selected {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			continue
		}
		n, err := s.App.EnqueueAllPending(r.Context(), id)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		if n > 0 {
			series++
			chapters += n
		}
	}
	slog.Info("queued chapters for several series", "series", series, "chapters", chapters)
	if chapters == 0 {
		// "Queued 0 chapters from 0 series" is a non-answer: it reports the
		// arithmetic instead of saying nothing needed doing.
		s.render(w, r, ui.Notice("Nothing to fetch — those are already downloaded.", ""))
		return
	}
	s.render(w, r, ui.Notice(fmt.Sprintf("Queued %s from %s.",
		ui.Count(chapters, "chapter", "chapters"), ui.Count(series, "series", "series")), ""))
}

// handleRemove shows the confirmation, and handleRemoveSeries does it.
//
// The confirmation is a page block rather than a browser dialog because the
// question a reader has is whether their downloaded files are about to go,
// and confirm() cannot answer it.
func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
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
	v := s.seriesView(r.Context(), series, chapters)
	if r.URL.Query().Get("cancel") != "" {
		return // an empty reply clears the confirmation
	}
	s.render(w, r, ui.ConfirmRemove(v))
}

// handleRemoveSeries forgets a series, leaving every file it wrote in place.
func (s *Server) handleRemoveSeries(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	series, err := s.App.Store.GetSeries(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.App.Store.DeleteSeries(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	slog.Info("removed series", "id", id, "title", series.Title, "files", "kept")
	// The page being looked at no longer describes anything.
	w.Header().Set("HX-Redirect", "/")
	w.WriteHeader(http.StatusNoContent)
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
	s.render(w, r, ui.SeriesActions(s.seriesView(r.Context(), series, chapters)))
}

// handleCheckNow asks the scheduler for an immediate sweep.
func (s *Server) handleCheckNow(w http.ResponseWriter, r *http.Request) {
	s.App.Scheduler.CheckNow()
	w.WriteHeader(http.StatusNoContent)
}

// handleQueue serves the status line, which the footer fetches on load.
func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, ui.QueueStatus(s.queueView(r.Context())))
}

// handlePauseQueue stops new downloads from starting; running ones finish.
func (s *Server) handlePauseQueue(w http.ResponseWriter, r *http.Request) {
	s.App.PauseQueue()
	s.App.Bus.Publish(jobs.Event{Kind: "queue-updated"})
	s.render(w, r, ui.QueueStatus(s.queueView(r.Context())))
}

// handleResumeQueue lets queued downloads start again.
func (s *Server) handleResumeQueue(w http.ResponseWriter, r *http.Request) {
	s.App.ResumeQueue()
	s.App.Bus.Publish(jobs.Event{Kind: "queue-updated"})
	s.render(w, r, ui.QueueStatus(s.queueView(r.Context())))
}

// queueView gathers what the footer shows. The counts come from the store,
// the pages from the downloads running in this process.
func (s *Server) queueView(ctx context.Context) ui.QueueView {
	q, err := s.App.Store.Queue(ctx)
	if err != nil {
		slog.Error("queue status", "err", err)
	}
	v := ui.QueueView{QueueStatus: q, Paused: s.App.QueuePaused()}
	for _, p := range s.App.ActiveDownloads() {
		v.Done += p.Done
		v.Total += p.Total
	}
	return v
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

// renderSeriesActions re-renders a series' header actions after a change.
func (s *Server) renderSeriesActions(w http.ResponseWriter, r *http.Request, id int64) {
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
	s.render(w, r, ui.SeriesActions(s.seriesView(r.Context(), series, chapters)))
}
