package download

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// chapterNum pulls the first number out of a chapter name. Site naming is
// wildly inconsistent ("Chapter 12", "Ch.12", "12 - Title", "第12話"), so the
// first run of digits is the only reliable signal.
var chapterNum = regexp.MustCompile(`(\d+(?:\.\d+)?)`)

// volumeNum matches an explicit volume marker.
var volumeNum = regexp.MustCompile(`(?i)\bvol(?:ume)?\.?\s*(\d+)`)

// ParseChapter derives the normalised number and volume from a chapter name.
//
// The number is zero-padded to three digits so that lexical sorting matches
// numeric order, which is what Komga and every CBZ reader rely on. A decimal
// chapter (12.5) keeps its fraction.
func ParseChapter(series, name string) Chapter {
	c := Chapter{Series: series, Name: name}

	rest := name
	if m := volumeNum.FindStringSubmatch(name); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil {
			c.Volume = fmt.Sprintf("%02d", n)
		}
		rest = strings.Replace(name, m[0], "", 1)
	}

	if m := chapterNum.FindStringSubmatch(rest); m != nil {
		c.Number = padChapter(m[1])
	} else {
		c.Number = "000"
	}
	return c
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
