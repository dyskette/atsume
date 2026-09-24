package download

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ComicInfo is the metadata file readers look for inside a CBZ.
//
// A file name cannot carry a title. Sanitize strips the characters a
// filesystem forbids, so "1/2 Prince" becomes the directory "12 Prince", and
// a library server reading only the folder shows that — a different work.
// Long titles are truncated and non-Latin ones survive only by luck of the
// filesystem's encoding. This file states what the thing actually is.
//
// The element names and their order follow the ComicInfo 2.0 schema. Komga
// is lenient about order; other readers are not, and the cost of matching is
// nothing.
type ComicInfo struct {
	XMLName xml.Name `xml:"ComicInfo"`
	XSI     string   `xml:"xmlns:xsi,attr"`
	XSD     string   `xml:"xmlns:xsd,attr"`

	Title      string `xml:"Title,omitempty"`
	Series     string `xml:"Series,omitempty"`
	Number     string `xml:"Number,omitempty"`
	Count      int    `xml:"Count,omitempty"`
	Summary    string `xml:"Summary,omitempty"`
	Notes      string `xml:"Notes,omitempty"`
	Writer     string `xml:"Writer,omitempty"`
	Penciller  string `xml:"Penciller,omitempty"`
	Genre      string `xml:"Genre,omitempty"`
	Web        string `xml:"Web,omitempty"`
	PageCount  int    `xml:"PageCount,omitempty"`
	Manga      string `xml:"Manga,omitempty"`
	Characters string `xml:"Characters,omitempty"`
}

// comicInfoName is where readers expect the file: the archive root.
const comicInfoName = "ComicInfo.xml"

// SeriesMeta is what a chapter's archive should say about the work it belongs
// to, in the site's own words rather than the filesystem's.
type SeriesMeta struct {
	Title   string
	Summary string
	Authors string
	Artists string
	Genres  string
	Status  string
	// URL is where the series came from, and Site who it came from. Both go
	// into the notes, so a file found on disk in a year can be traced.
	URL  string
	Site string
	// Chapters is how many the site lists. It is only written when the work
	// is finished, because a count that keeps changing tells a reader their
	// library is incomplete when it is merely ongoing.
	Chapters int
}

// BuildComicInfo describes one chapter for the library server that will read
// it.
//
// Volume is deliberately absent. Komga appends a volume above 1 to the series
// title, and it aggregates that across the books in a folder, so a series
// whose chapters span volumes would be titled after whichever book was read
// last. The volume stays in the file name, where it belongs and where Komga
// already parses it.
func BuildComicInfo(s SeriesMeta, c Chapter, pages int) *ComicInfo {
	info := &ComicInfo{
		XSI:       "http://www.w3.org/2001/XMLSchema-instance",
		XSD:       "http://www.w3.org/2001/XMLSchema",
		Title:     strings.TrimSpace(c.Name),
		Series:    strings.TrimSpace(s.Title),
		Number:    chapterSortNumber(c),
		Summary:   strings.TrimSpace(s.Summary),
		Writer:    strings.TrimSpace(s.Authors),
		Penciller: strings.TrimSpace(s.Artists),
		Genre:     strings.TrimSpace(s.Genres),
		Web:       strings.TrimSpace(c.URL),
		PageCount: pages,
	}
	if strings.EqualFold(strings.TrimSpace(s.Status), "completed") && s.Chapters > 0 {
		info.Count = s.Chapters
	}
	info.Notes = comicInfoNotes(s)
	return info
}

// chapterSortNumber is the ComicInfo number for c.
//
// Komga orders a series by this number alone, falling back to the file name
// only to break ties, and the name sorts by chapter before volume. A series
// whose episodes restart each season — WebToons names them "[Season 2] Ep. 1"
// — therefore reads S1E1, S2E1, S3E1, S1E2 unless the season is part of the
// number, so a season chapter's number is "2.001": season, then the episode
// padded as in the file name. Checked against Komga, which then keeps each
// season together and in order.
//
// A real volume needs none of this, since chapter numbers carry on across
// volumes. An episode the scheme cannot order, one with a fraction or past
// 999, keeps its plain number rather than a misleading one.
func chapterSortNumber(c Chapter) string {
	if seasonNum.MatchString(c.Name) && !volumeNum.MatchString(c.Name) && c.Volume != "" &&
		len(c.Number) == 3 && strings.Trim(c.Number, "0123456789") == "" {
		if season, err := strconv.Atoi(c.Volume); err == nil {
			return strconv.Itoa(season) + "." + c.Number
		}
	}
	return comicInfoNumber(c.Number)
}

// comicInfoNumber renders a chapter number the way the format expects.
//
// The file name pads to three digits so a directory listing sorts; the
// metadata does not, because a reader displays this verbatim and "001" is
// not what anyone calls chapter one.
func comicInfoNumber(n string) string {
	n = strings.TrimSpace(n)
	if n == "" {
		return ""
	}
	// Only touch something that is plainly a number, so "12.5" and "Extra"
	// both survive intact.
	if f, err := strconv.ParseFloat(n, 64); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64)
	}
	return n
}

// comicInfoNotes records where the file came from.
//
// A CBZ outlives the thing that made it. Someone looking at this file in a
// year should be able to tell what produced it and from where without
// guessing.
func comicInfoNotes(s SeriesMeta) string {
	parts := []string{"Downloaded by atsume on " + time.Now().UTC().Format(time.DateOnly)}
	if site := strings.TrimSpace(s.Site); site != "" {
		parts = append(parts, "from "+site)
	}
	if u := strings.TrimSpace(s.URL); u != "" {
		parts = append(parts, u)
	}
	return strings.Join(parts, " · ")
}

// Marshal renders the file as a reader expects to find it.
func (c *ComicInfo) Marshal() ([]byte, error) {
	body, err := xml.MarshalIndent(c, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("comicinfo: %w", err)
	}
	return append([]byte(xml.Header), append(body, '\n')...), nil
}
