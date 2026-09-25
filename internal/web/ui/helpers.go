// Package ui holds the templ components for the web interface.
package ui

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/app"
	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
)

// SiteURL is the address of one site's pages.
//
// A site is named by whatever the module declares, and those names contain
// spaces, brackets and Han characters — "Colorcito Scan (Afiliados)", "包子漫畫
// (BaozimhOrg)". They are path segments, so they are escaped as such.
func SiteURL(site string, suffix ...string) string {
	out := "/modules/" + url.PathEscape(site)
	for _, s := range suffix {
		out += s
	}
	return out
}

// pageURL builds a directory URL for a module page.
func pageURL(module string, page int) string {
	if page <= 0 {
		return SiteURL(module)
	}
	return SiteURL(module, fmt.Sprintf("?page=%d", page))
}

// previewURL is a look at a series before committing to it.
func previewURL(module, link string) string {
	return SiteURL(module, "/preview?url="+escapeQueryValue(link))
}

// lastChecked renders when a series was last looked at, which is the only
// signal that automatic checking is actually running.
func lastChecked(s store.Series) string {
	if !s.CheckedAt.Valid {
		return "never checked"
	}
	d := time.Since(s.CheckedAt.Time)
	switch {
	case d < time.Minute:
		return "checked just now"
	case d < time.Hour:
		return fmt.Sprintf("checked %dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("checked %dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("checked %dd ago", int(d.Hours()/24))
	}
}

// optionLabel prefers the caption a module supplied, falling back to its key.
func optionLabel(o scraper.Option) string {
	if o.Caption != "" {
		return o.Caption
	}
	return o.Name
}

// passwordPlaceholder hints whether a password is already stored, without
// revealing anything about it.
func passwordPlaceholder(s *app.ModuleSettings) string {
	if s.HasCredentials {
		return "unchanged"
	}
	return ""
}

// QueueView is what the footer shows: the chapters in flight, whether the
// queue is paused, and how many pages the running downloads have in.
type QueueView struct {
	store.QueueStatus
	Paused      bool
	Done, Total int
}

// queueText says what is happening in the fewest words that are still
// specific: a count alone reads as a number with no verb.
func queueText(v QueueView) string {
	var parts []string
	if v.Downloading > 0 {
		if v.Paused {
			parts = append(parts, fmt.Sprintf("%d finishing", v.Downloading))
		} else {
			parts = append(parts, fmt.Sprintf("%d active", v.Downloading))
		}
	}
	if v.Queued > 0 {
		parts = append(parts, fmt.Sprintf("%d queued", v.Queued))
	}
	head := "Downloading"
	if v.Paused {
		head = "Paused"
	}
	if len(parts) == 0 {
		return head
	}
	return head + " · " + strings.Join(parts, ", ")
}

// truncate shortens a message for inline display, keeping the full text in the
// element's title attribute.
func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// Count renders a number with its noun, pluralised. Writing "1 chapter(s)"
// makes the reader do the work the interface should have done.
func Count(n int, singular, plural string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, plural)
}

// areOrIs keeps a sentence grammatical whichever number lands in it.
func areOrIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

// queryEscape makes a search term safe in a URL.
func queryEscape(s string) string { return url.QueryEscape(s) }

// escapeQueryValue escapes a value for a query string without mangling the
// characters a query is allowed to contain.
//
// RFC 3986 defines a query as pchar / "/" / "?", so a slash and a colon need
// no escaping there. url.QueryEscape escapes them anyway, which turns a
// perfectly readable address into
//
//	?url=%2Ftitle%2Fda0ccc81-68ef-4b0b-8023-52f60046d714
//
// The alternative — putting the path in the URL itself — reads better still
// for the four fifths of links that are plain paths, and needs a second URL
// shape for the rest. One shape that handles every link is worth more than
// the last of the tidiness.
func escapeQueryValue(s string) string {
	e := url.QueryEscape(s)
	e = strings.ReplaceAll(e, "%2F", "/")
	e = strings.ReplaceAll(e, "%3A", ":")
	return e
}

// titleLabel is what a catalogue row is called.
//
// A module can return a link with no name — the site moved the title into an
// attribute, or the markup changed under a module nobody has updated. The
// row still has to be readable and clickable, so it falls back to the
// address rather than rendering as a blank strip with a Follow button.
func titleLabel(t store.SiteTitle) string {
	if name := strings.TrimSpace(t.Name); name != "" {
		return name
	}
	if t.URL != "" {
		return t.URL
	}
	return "Untitled"
}

// entryLabel is what a directory row is called.
//
// A module can return a link with no name — the site moved the title into an
// attribute, or the markup changed under a module nobody has updated. The
// row still has to be readable and clickable, so it falls back to the
// address rather than rendering as a blank strip with a Follow button.
func entryLabel(e scraper.Entry) string {
	if name := strings.TrimSpace(e.Name); name != "" {
		return name
	}
	if e.Link != "" {
		return e.Link
	}
	return "Untitled"
}

// olderThan renders an age as the blunt version, for a list old enough that
// a month and a year leaves the reader doing arithmetic.
func olderThan(t time.Time) string {
	months := int(time.Since(t).Hours() / 24 / 30)
	if months >= 24 {
		return fmt.Sprintf("%d years old", months/12)
	}
	if months >= 12 {
		return "over a year old"
	}
	return fmt.Sprintf("%d months old", months)
}
