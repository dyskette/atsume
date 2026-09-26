package scraper

import "fmt"

// FetchError is a module reporting a network problem, with what the last
// request it made got back. The message is what it always was; the fields
// let a caller say why in plain words instead of quoting it.
type FetchError struct {
	Module string
	// What is what the module was doing: "listing page 3", "fetching …".
	What string
	// Status is the last HTTP status, 0 when the site did not answer.
	Status int
	// Challenge reports that a response carried an anti-bot interstitial.
	Challenge bool
	// Cause is why the site did not answer at all, nil when it did.
	Cause error
	// URL is where the last request ended up, after redirects, and
	// Requested what it asked for: the address to look at when a module
	// reports a problem without saying what it requested.
	URL, Requested string
}

func (e *FetchError) Error() string {
	msg := fmt.Sprintf("%s: network problem %s (last status %d", e.Module, e.What, e.Status)
	if e.Requested != "" {
		msg += " from " + e.Requested
	}
	msg += ")"
	if e.Cause != nil {
		msg += ": " + e.Cause.Error()
	}
	return msg
}

func (e *FetchError) Unwrap() error { return e.Cause }

// fetchError describes the network problem a handler just reported.
func (r *Runner) fetchError(what string) *FetchError {
	return &FetchError{
		Module: r.mod.Name, What: what,
		Status: r.http.ResultCode, Challenge: r.http.Challenged,
		Cause: r.http.LastErr, URL: r.http.LastURL, Requested: r.http.RequestURL,
	}
}

// Challenged reports whether any response this runner received carried an
// anti-bot interstitial.
func (r *Runner) Challenged() bool { return r.http.Challenged }

// Redirected reports the first request a redirect took to another host: the
// host asked for and the address it ended at, both "" when none was.
func (r *Runner) Redirected() (from, to string) { return r.http.RedirectFrom, r.http.RedirectTo }
