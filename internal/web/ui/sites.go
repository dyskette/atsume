package ui

import (
	"sort"
	"strings"

	"github.com/dyskette/atsume/internal/app"
)

// SiteGroup is one category of sites.
type SiteGroup struct {
	Category string
	Entries  []app.ModuleEntry
}

// SitesView is the site chooser.
type SitesView struct {
	Ref   string
	Total int
	Query string
	// Groups is the list by category. When a search is active it holds a
	// single unnamed group, because grouping a handful of matches only makes
	// them harder to scan.
	Groups []SiteGroup
	Found  int
}

// uncategorised is where modules that declare nothing go. Named rather than
// hidden: a reader looking for a site they know would otherwise think it
// missing.
const uncategorised = "Uncategorised"

// BuildSites filters and groups the catalogue.
func BuildSites(c app.Catalogue, query string) SitesView {
	v := SitesView{Ref: c.Ref, Total: len(c.Entries), Query: query}

	q := strings.ToLower(strings.TrimSpace(query))
	var matched []app.ModuleEntry
	for _, e := range c.Entries {
		if q == "" || strings.Contains(strings.ToLower(e.Name), q) ||
			strings.Contains(strings.ToLower(e.Category), q) {
			matched = append(matched, e)
		}
	}
	v.Found = len(matched)

	if q != "" {
		v.Groups = []SiteGroup{{Entries: matched}}
		return v
	}

	byCategory := map[string][]app.ModuleEntry{}
	for _, e := range matched {
		key := e.Category
		if key == "" {
			key = uncategorised
		}
		byCategory[key] = append(byCategory[key], e)
	}

	names := make([]string, 0, len(byCategory))
	for name := range byCategory {
		names = append(names, name)
	}
	// Largest first: the categories most people want are the big ones, and
	// alphabetical order would bury them.
	sort.Slice(names, func(i, j int) bool {
		if len(byCategory[names[i]]) != len(byCategory[names[j]]) {
			return len(byCategory[names[i]]) > len(byCategory[names[j]])
		}
		return names[i] < names[j]
	})
	// Whatever has no category goes last regardless of size.
	sort.SliceStable(names, func(i, j int) bool {
		return names[j] == uncategorised && names[i] != uncategorised
	})

	for _, name := range names {
		v.Groups = append(v.Groups, SiteGroup{Category: name, Entries: byCategory[name]})
	}
	return v
}
