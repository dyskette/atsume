package ui

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"

	"github.com/dyskette/atsume/internal/download"
	"github.com/dyskette/atsume/internal/store"
)

// RowState is what a chapter row shows beyond the chapter's stored state.
type RowState struct {
	// Position is a queued chapter's place in line, 1 being next.
	Position int
	// Active is whether its download is running now, with Done of Total
	// pages in. Total is 0 while the page list is still being fetched.
	Active      bool
	Done, Total int
}

// Chapter list filters.
const (
	FilterAll           = ""
	FilterDownloaded    = "downloaded"
	FilterNotDownloaded = "not-downloaded"
)

// rowLimit is how many chapters show before "Show all".
const rowLimit = 12

// numberedPositions is how far down the queue a row shows its place in line.
// Beyond it a row just says Queued, so a long queue does not mean updating
// every row each time one chapter starts.
const numberedPositions = 10

// downloaded reports whether a chapter's file is on disk.
func (v SeriesView) downloaded(c store.Chapter) bool {
	return c.State == store.ChapterDone && !v.Missing[c.ID]
}

// DownloadedCount is how many chapters are on disk.
func (v SeriesView) DownloadedCount() int {
	n := 0
	for _, c := range v.Chapters {
		if v.downloaded(c) {
			n++
		}
	}
	return n
}

// NotDownloadedCount is how many chapters are not on disk.
func (v SeriesView) NotDownloadedCount() int { return v.ChapterTotal() - v.DownloadedCount() }

// FilteredTotal is how many chapters the current filter matches.
func (v SeriesView) FilteredTotal() int {
	switch v.Filter {
	case FilterDownloaded:
		return v.DownloadedCount()
	case FilterNotDownloaded:
		return v.NotDownloadedCount()
	}
	return v.ChapterTotal()
}

// Hidden is how many matching chapters the limit leaves out.
func (v SeriesView) Hidden() int {
	if v.ShowAll {
		return 0
	}
	return max(v.FilteredTotal()-rowLimit, 0)
}

// sortKey orders chapters by volume, then number. Sites do not give dates,
// and the order they list chapters in varies from module to module, so the
// numbers atsume parses are what "newest" can mean.
type sortKey struct{ volume, number float64 }

func (k sortKey) less(o sortKey) bool {
	if k.volume != o.volume {
		return k.volume < o.volume
	}
	return k.number < o.number
}

func keyOf(number, volume string) sortKey {
	n, _ := strconv.ParseFloat(number, 64)
	vol, _ := strconv.ParseFloat(volume, 64)
	return sortKey{vol, n}
}

// order returns indexes into a list of n chapters, newest first unless
// oldest is set. Chapters with the same number keep the order the site
// listed them in.
func order(n int, key func(int) sortKey, oldest bool) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool {
		if oldest {
			return key(idx[a]).less(key(idx[b]))
		}
		return key(idx[b]).less(key(idx[a]))
	})
	return idx
}

// VisibleChapters is the stored chapters the list shows: filtered, sorted and
// limited.
func (v SeriesView) VisibleChapters() []store.Chapter {
	idx := order(len(v.Chapters), func(i int) sortKey {
		return keyOf(v.Chapters[i].Number, v.Chapters[i].Volume)
	}, v.Oldest)
	var out []store.Chapter
	for _, i := range idx {
		c := v.Chapters[i]
		switch {
		case v.Filter == FilterDownloaded && !v.downloaded(c),
			v.Filter == FilterNotDownloaded && v.downloaded(c):
			continue
		}
		if !v.ShowAll && len(out) == rowLimit {
			break
		}
		out = append(out, c)
	}
	return out
}

// VisibleListed is the same for a series not in the library, none of whose
// chapters is downloaded.
func (v SeriesView) VisibleListed() []ListedChapter {
	if v.Filter == FilterDownloaded {
		return nil
	}
	keys := make([]sortKey, len(v.Listed))
	for i, c := range v.Listed {
		p := download.ParseChapter(v.Series.Title, c.Name)
		keys[i] = keyOf(p.Number, p.Volume)
	}
	var out []ListedChapter
	for _, i := range order(len(v.Listed), func(i int) sortKey { return keys[i] }, v.Oldest) {
		if !v.ShowAll && len(out) == rowLimit {
			break
		}
		out = append(out, v.Listed[i])
	}
	return out
}

// FirstToDownload is the earliest chapter not downloaded, for "Start with
// chapter 1": a stored chapter for a series in the library, a listed one
// otherwise. ok is false when there is none.
func (v SeriesView) FirstToDownload() (stored store.Chapter, listed ListedChapter, ok bool) {
	oldest := v
	oldest.Oldest, oldest.ShowAll, oldest.Filter = true, true, FilterNotDownloaded
	if v.Tracked() {
		if chs := oldest.VisibleChapters(); len(chs) > 0 {
			return chs[0], ListedChapter{}, true
		}
		return store.Chapter{}, ListedChapter{}, false
	}
	if l := oldest.VisibleListed(); len(l) > 0 {
		return store.Chapter{}, l[0], true
	}
	return store.Chapter{}, ListedChapter{}, false
}

// ListURL is this page with the given filter, order and limit.
func (v SeriesView) ListURL(filter string, oldest, all bool) string {
	q := url.Values{}
	base := fmt.Sprintf("/series/%d", v.Series.ID)
	if !v.Tracked() {
		base = "/modules/" + v.Module + "/preview"
		q.Set("url", v.SeriesURL)
	}
	if filter != FilterAll {
		q.Set("filter", filter)
	}
	if oldest {
		q.Set("sort", "oldest")
	}
	if all {
		q.Set("all", "1")
	}
	if len(q) == 0 {
		return base
	}
	return base + "?" + q.Encode()
}

// QueueLabel is a queued row's place in line.
func (st RowState) QueueLabel() string {
	switch {
	case st.Position == 1:
		return "Queued · up next"
	case st.Position > 1 && st.Position <= numberedPositions:
		return fmt.Sprintf("Queued · #%d", st.Position)
	default:
		return "Queued"
	}
}

// NumberedPosition reports whether a row at this place in line shows it.
func NumberedPosition(p int) bool { return p >= 1 && p <= numberedPositions }
