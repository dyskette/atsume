package download

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// chapterMarker finds a number introduced as a chapter or episode, in the
// languages the catalogue uses: "Chapter 12", "Ch.12", "Capítulo 3",
// "Episode 7", "Ep. 0", "Глава 5", "#45", "第12話". A name often carries other
// numbers — "Season 2 Chapter 10", "Kaiju No. 8 Chapter 100" — so a marked
// number wins over the first one.
//
// Go's \b only knows ASCII letters, so a word marker is preceded by the start
// or a non-letter instead, which also works for Cyrillic.
var chapterMarker = regexp.MustCompile(
	`(?i)(?:^|[^\p{L}])(?:chapter|chapitre|chap|ch|cap[ií]tulo|cap|episodio|episode|ep|глава)\.?\s*#?\s*(\d+(?:\.\d+)?)` +
		`|(?:#|第)\s*(\d+(?:\.\d+)?)`)

// chapterNum pulls the first number out of a chapter name, the fallback when
// nothing is marked ("12 - Title").
var chapterNum = regexp.MustCompile(`(\d+(?:\.\d+)?)`)

// volumeNum matches an explicit volume marker, or failing that a season:
// WebToons names episodes "[Season 2] Ep. 1" and restarts the count each
// season, so the season is what tells two "Ep. 1" apart, which is the job the
// volume does in a file name.
var volumeNum = regexp.MustCompile(`(?i)\bvol(?:ume)?\.?\s*(\d+)`)
var seasonNum = regexp.MustCompile(`(?i)\bseason\s*(\d+)`)

// ParseChapter derives the normalised number and volume from a chapter name.
//
// The series title is ignored when the name starts with it, since a title
// such as "Kaiju No. 8" would otherwise supply the number. After that a
// marked number wins, then the first number, then "000" for a name with none,
// such as "Oneshot". Chapters that still end up sharing a number are told
// apart when their files are named; see Chapter.Candidates.
//
// The number is zero-padded to three digits so that lexical sorting matches
// numeric order, which is what Komga and every CBZ reader rely on. A decimal
// chapter (12.5) keeps its fraction.
func ParseChapter(series, name string) Chapter {
	c := Chapter{Series: series, Name: name}

	rest := name
	m := volumeNum.FindStringSubmatch(name)
	if m == nil {
		m = seasonNum.FindStringSubmatch(name)
	}
	if m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			c.Volume = fmt.Sprintf("%02d", n)
		}
		rest = strings.Replace(name, m[0], "", 1)
	}
	rest = trimSeriesPrefix(strings.TrimSpace(rest), series)

	c.Number = "000"
	if m := chapterMarker.FindStringSubmatch(rest); m != nil {
		c.Number = padChapter(m[1] + m[2]) // exactly one group matched
	} else if m := chapterNum.FindStringSubmatch(rest); m != nil {
		c.Number = padChapter(m[1])
	}
	return c
}

// trimSeriesPrefix drops the series title from the start of a chapter name,
// ignoring case.
func trimSeriesPrefix(name, series string) string {
	series = strings.TrimSpace(series)
	if series == "" || len(name) < len(series) || !strings.EqualFold(name[:len(series)], series) {
		return name
	}
	return name[len(series):]
}

// padChapter zero-pads the integer part to three digits, keeping any fraction.
func padChapter(s string) string {
	whole, frac, hasFrac := strings.Cut(s, ".")
	n, err := strconv.Atoi(whole)
	if err != nil {
		return s
	}
	out := fmt.Sprintf("%03d", n)
	if hasFrac {
		out += "." + frac
	}
	return out
}
