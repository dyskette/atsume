package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"
	"testing"

	"github.com/dyskette/atsume/internal/scraper"
)

func TestClassifyProblem(t *testing.T) {
	fetch := func(status int, challenge bool, cause error) error {
		return fmt.Errorf("read: %w", &scraper.FetchError{Module: "M", What: "listing page 1", Status: status, Challenge: challenge, Cause: cause})
	}
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"none", nil, ""},
		{"stopped", context.Canceled, ""},
		{"challenge wins over its status", fetch(503, true, nil), ProblemBlocked},
		{"refused", fetch(403, false, nil), ProblemBlocked},
		{"unauthorised", fetch(401, false, nil), ProblemBlocked},
		{"not found", fetch(404, false, nil), ProblemMoved},
		{"gone", fetch(410, false, nil), ProblemMoved},
		{"server error", fetch(502, false, nil), ProblemDown},
		{"rate limited", fetch(429, false, nil), ProblemDown},
		{"no answer", fetch(0, false, errors.New("no such host")), ProblemUnreachable},
		{"other status", fetch(400, false, nil), ""},
		{"lua error", errors.New("attempt to index a nil value"), ProblemBroken},
	}
	for _, c := range cases {
		if got := classifyProblem(c.err); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

type timeoutErr struct{}

func (timeoutErr) Error() string   { return "i/o timeout" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

func TestFailureOfNamesTheCause(t *testing.T) {
	fetch := func(cause error) error {
		return &scraper.FetchError{Module: "M", What: "listing page 1", Cause: cause}
	}
	cases := []struct {
		err  error
		want string
	}{
		{fetch(&url.Error{Op: "Get", URL: "http://x", Err: &net.OpError{Op: "dial", Err: &net.DNSError{Err: "no such host", Name: "x"}}}), "dns"},
		{fetch(&url.Error{Op: "Get", URL: "http://x", Err: &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}}), "refused"},
		{fetch(&url.Error{Op: "Get", URL: "http://x", Err: timeoutErr{}}), "timeout"},
		{fetch(nil), ""},
		{errors.New("attempt to index a nil value"), ""},
	}
	for _, c := range cases {
		if got := failureOf(c.err).Cause; got != c.want {
			t.Errorf("%v: cause %q, want %q", c.err, got, c.want)
		}
	}
}
