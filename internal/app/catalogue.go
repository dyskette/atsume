package app

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dyskette/atsume/internal/scraper"
)

// ModuleEntry is one website as the chooser lists it.
//
// A website is not a module file. Twenty-eight files declare several: some
// hold genuinely different sites — E-Hentai and ExHentai live in one file and
// only the second takes a login — and some hold a dozen mirrors of one site.
// Treating the file as the website exposed whichever was declared last and
// hid the other sixty-one entirely.
type ModuleEntry struct {
	// Site is the name the module declares, which is the identity: 658 of
	// them across the catalogue, with no two alike. It is both what the
	// reader sees and what URLs and stored series refer to.
	Site string
	// File is the path the site is declared in, which is all a file is now
	// for: somewhere to load the Lua from.
	File string
	// FileName is that file's bare name. It was the identifier before a file
	// was understood to hold more than one site, so series followed earlier
	// still refer to sites by it.
	FileName string
	// ID is the identifier upstream assigns, kept so a site can still be
	// recognised after it is renamed.
	ID string
	// Category is what the module declares — "English", "Webcomics",
	// "English-Scanlation", "Raw". It is the only structure available for a
	// list of several hundred sites.
	Category string
	// NeedsLogin reports that the module implements OnLogin. A site that
	// gates its chapter list behind an account returns a page that parses
	// perfectly and lists nothing, so knowing this is the difference between
	// "no chapters found" and "this site needs an account".
	NeedsLogin bool
	// Mirrors are the addresses this site is declared under, in order. Most
	// sites have one. MangaPark has fourteen, and they exist because these
	// domains are blocked and abandoned constantly.
	Mirrors []string
	// Err is why the file would not load, when it would not. Such a site is
	// still listed — a reader looking for one they know would otherwise
	// think atsume had never heard of it — but it is listed as unavailable
	// rather than looking exactly like one that works until it is clicked.
	Err string
}

// Available reports whether the site can be opened at all.
func (e ModuleEntry) Available() bool { return e.Err == "" }

// HasMirrors reports whether there is a choice of address to make.
func (e ModuleEntry) HasMirrors() bool { return len(e.Mirrors) > 1 }

// Catalogue is the site list.
type Catalogue struct {
	Entries []ModuleEntry
	// Ref is the module revision it was read from.
	Ref string
}

// catalogueCache holds the last scan. Reading a category means running a
// module's Init(), so several hundred of them are scanned once and kept.
type catalogueCache struct {
	mu      sync.Mutex
	ref     string
	entries []ModuleEntry
}

// SiteInfo returns what is known about one site without loading it.
//
// It accepts the site name, the identifier upstream assigns, or — for series
// followed before a file was understood to hold more than one site — the file
// name, which then resolves to that file's first site.
func (a *App) SiteInfo(ctx context.Context, name string) (ModuleEntry, bool) {
	entries := a.ModuleCatalogue(ctx).Entries
	for _, e := range entries {
		if strings.EqualFold(e.Site, name) {
			return e, true
		}
	}
	for _, e := range entries {
		if e.ID != "" && strings.EqualFold(e.ID, name) {
			return e, true
		}
	}
	// A file declaring several sites resolves to its first, which is the only
	// answer available: the series was followed when the file was the site.
	for _, e := range entries {
		if strings.EqualFold(e.FileName, name) {
			return e, true
		}
	}
	return ModuleEntry{}, false
}

// ResolveModule maps whatever a caller has onto the site name everything else
// is keyed by.
func (a *App) ResolveModule(ctx context.Context, name string) string {
	if e, ok := a.SiteInfo(ctx, name); ok {
		return e.Site
	}
	return name
}

// ModuleCatalogue lists every site with its declared category.
//
// The scan opens each file, which takes well under a second for the whole
// catalogue, and is cached until the pinned revision changes.
func (a *App) ModuleCatalogue(ctx context.Context) Catalogue {
	ref := a.Registry.Ref()

	a.catalogue.mu.Lock()
	defer a.catalogue.mu.Unlock()
	if a.catalogue.ref == ref && a.catalogue.entries != nil {
		return Catalogue{Entries: a.catalogue.entries, Ref: ref}
	}

	start := time.Now()
	files := a.Registry.Modules()
	entries := make([]ModuleEntry, 0, len(files))
	for _, f := range files {
		r, err := a.openFileRaw(ctx, f.File, "", "")
		if err != nil {
			// A file that will not load still belongs in the list under the
			// only name available — the file's. Any sites it declared before
			// it failed are lost with it, which is why a broken file can
			// cost more than one site.
			entries = append(entries, ModuleEntry{
				Site: f.Name, File: f.File, FileName: f.Name, Err: err.Error(),
			})
			continue
		}
		entries = append(entries, sitesOf(r.Sites(), f.File, f.Name)...)
		r.Close()
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Site) < strings.ToLower(entries[j].Site)
	})

	slog.Debug("scanned the catalogue", "files", len(files), "sites", len(entries),
		"took", time.Since(start))
	a.catalogue.ref, a.catalogue.entries = ref, entries
	return Catalogue{Entries: entries, Ref: ref}
}

// sitesOf turns one file's declarations into catalogue entries, folding
// mirrors together.
//
// Declarations that share a name are the same site under several addresses —
// MangaPark declares fourteen — and listing each one separately would offer a
// reader fourteen identical choices. Declarations with different names are
// different sites and each get an entry.
func sitesOf(sites []*scraper.Module, file, fileName string) []ModuleEntry {
	var out []ModuleEntry
	byName := map[string]int{}
	for _, m := range sites {
		name := strings.TrimSpace(m.Name)
		if name == "" {
			name = fileName
		}
		if at, seen := byName[strings.ToLower(name)]; seen {
			out[at].Mirrors = append(out[at].Mirrors, m.RootURL)
			// A login declared on any mirror is a login for the site.
			if _, ok := m.Handler("OnLogin"); ok {
				out[at].NeedsLogin = true
			}
			continue
		}
		_, login := m.Handler("OnLogin")
		byName[strings.ToLower(name)] = len(out)
		out = append(out, ModuleEntry{
			Site:       name,
			File:       file,
			FileName:   fileName,
			ID:         m.ID,
			Category:   strings.TrimSpace(m.Category),
			NeedsLogin: login,
			Mirrors:    []string{m.RootURL},
		})
	}
	return out
}

// RepairModuleKeys rewrites series that refer to a site by the file it lives
// in.
//
// A file was the identity until a file was understood to hold several sites,
// so rows written earlier hold a file name. Lookups still resolve those, but
// leaving them is how the last identity bug survived unnoticed for months:
// data that means one thing and says another. Anything that cannot be
// resolved is left alone and reported, because a row pointing at a module
// that upstream has removed is worth seeing rather than rewriting.
func (a *App) RepairModuleKeys(ctx context.Context) error {
	all, err := a.Store.ListSeries(ctx)
	if err != nil {
		return err
	}
	var fixed, unknown int
	for _, v := range all {
		key := v.Key()
		e, ok := a.SiteInfo(ctx, key)
		switch {
		case !ok:
			unknown++
			slog.Warn("series refers to an unknown site", "series", v.Title, "key", key)
			continue
		case e.Site == v.ModuleKey && e.Site == v.ModuleName:
			continue
		}
		// A file declaring several sites resolves by name first, so a row
		// that already knows which site it scraped keeps it.
		site := e.Site
		if v.ModuleName != "" {
			if byName, ok := a.SiteInfo(ctx, v.ModuleName); ok {
				site = byName.Site
			}
		}
		if err := a.Store.SetSeriesModule(ctx, v.ID, site); err != nil {
			return err
		}
		fixed++
	}
	if fixed > 0 || unknown > 0 {
		slog.Info("repaired series module keys", "fixed", fixed, "unresolved", unknown)
	}
	return nil
}
