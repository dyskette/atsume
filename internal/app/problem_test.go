package app

import (
	"context"
	"errors"
	"fmt"
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
