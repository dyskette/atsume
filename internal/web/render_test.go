package web

import (
	"errors"
	"strings"
	"testing"
)

// TestExplain pins the classification shown to the reader.
//
// Which cause it is decides what to do next — wait, change a setting, or report
// a bug — so the guess is worth making explicitly rather than leaving a Go
// error string on a blank page.
func TestExplain(t *testing.T) {
	cases := []struct {
		name      string
		err       string
		want      string
		wantHints bool
	}{
		{
			name:      "site unreachable",
			err:       `18Kami: network problem listing page 1 from https://18kami.com (last status 0)`,
			want:      "did not answer",
			wantHints: true,
		},
		{"dns failure", `Get "https://x": dial tcp: no such host`, "did not answer", true},
		{"timeout", `context deadline exceeded`, "did not answer", true},
		{"unknown module", `no module named "Nope" in revision master`, "not in the pinned revision", true},
		{"login", `EHentai login: rejected the credentials`, "could not sign in", true},
		{"module fault", `Madara/GetInfo: attempt to index a nil value`, "failed while running", true},
		{"unclassified", `something entirely unexpected`, "could not be completed", false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			summary, hints := explain(errors.New(c.err))
			if !strings.Contains(summary, c.want) {
				t.Errorf("summary = %q, want it to mention %q", summary, c.want)
			}
			if c.wantHints && len(hints) == 0 {
				t.Error("expected at least one hint")
			}
			if !c.wantHints && len(hints) != 0 {
				t.Errorf("expected no hints, got %v", hints)
			}
		})
	}
}
