package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/dyskette/atsume/internal/scraper"
)

// What kind of failure ended a site read. Each calls for something different
// from the reader, which is why the kind is kept apart from the message.
const (
	// ProblemBlocked is a site refusing atsume: 401 or 403, or an anti-bot
	// challenge page.
	ProblemBlocked = "blocked"
	// ProblemMoved is a 404 or 410 from the address atsume uses.
	ProblemMoved = "moved"
	// ProblemDown is the site's server failing, 5xx, or asking atsume to
	// slow down, 429. Both usually pass.
	ProblemDown = "down"
	// ProblemUnreachable is no answer at all.
	ProblemUnreachable = "unreachable"
	// ProblemBroken is the module failing while running, which is a fault in
	// the module or in atsume's support for it rather than in the site.
	ProblemBroken = "broken"
)

// classifyProblem says what kind of failure err is, "" when it is none of
// the kinds a reader can act on differently.
func classifyProblem(err error) string {
	if err == nil || errors.Is(err, context.Canceled) {
		return ""
	}
	var fe *scraper.FetchError
	if !errors.As(err, &fe) {
		return ProblemBroken
	}
	switch s := fe.Status; {
	case fe.Challenge:
		return ProblemBlocked
	case s == 0:
		return ProblemUnreachable
	case s == http.StatusUnauthorized, s == http.StatusForbidden:
		return ProblemBlocked
	case s == http.StatusNotFound, s == http.StatusGone:
		return ProblemMoved
	case s == http.StatusTooManyRequests, s >= 500:
		return ProblemDown
	}
	return ""
}
