package app

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// ModuleEntry is a site as the chooser lists it.
type ModuleEntry struct {
	Name string
	// Category is what the module declares — "English", "Webcomics",
	// "English-Scanlation", "Raw". It is the only structure available for a
	// list of several hundred sites, and it was previously discarded.
	Category string
}

// Catalogue is the site list, grouped.
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

// ModuleCatalogue lists every site with its declared category.
//
// The scan opens each module, which takes well under a second for the whole
// catalogue, and is cached until the pinned revision changes.
func (a *App) ModuleCatalogue(ctx context.Context) Catalogue {
	ref := a.Registry.Ref()

	a.catalogue.mu.Lock()
	defer a.catalogue.mu.Unlock()
	if a.catalogue.ref == ref && a.catalogue.entries != nil {
		return Catalogue{Entries: a.catalogue.entries, Ref: ref}
	}

	start := time.Now()
	mods := a.Registry.Modules()
	entries := make([]ModuleEntry, 0, len(mods))
	for _, m := range mods {
		entry := ModuleEntry{Name: m.Name}
		// A module that will not load still belongs in the list; it simply has
		// no category. Hiding it would be worse — the reader would wonder why
		// a site they know is missing.
		if r, err := a.openModuleRaw(ctx, m.Name); err == nil {
			entry.Category = strings.TrimSpace(r.Module().Category)
			r.Close()
		}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Name) < strings.ToLower(entries[j].Name)
	})

	slog.Debug("scanned module categories", "count", len(entries), "took", time.Since(start))
	a.catalogue.ref, a.catalogue.entries = ref, entries
	return Catalogue{Entries: entries, Ref: ref}
}
