package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"syscall"

	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/store"
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
	// ProblemStopped is not a failure: the reader stopped the read.
	ProblemStopped = "stopped"
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

// failureOf is what a read's error says about the site, for explaining it.
func failureOf(err error) store.Failure {
	var fe *scraper.FetchError
	if !errors.As(err, &fe) {
		return store.Failure{}
	}
	f := store.Failure{Status: fe.Status, Challenged: fe.Challenge}
	var dns *net.DNSError
	var ne net.Error
	switch {
	case fe.Cause == nil:
	case errors.As(fe.Cause, &dns):
		f.Cause = "dns"
	case errors.Is(fe.Cause, syscall.ECONNREFUSED):
		f.Cause = "refused"
	case errors.As(fe.Cause, &ne) && ne.Timeout():
		f.Cause = "timeout"
	}
	return f
}
