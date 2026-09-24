package scraper

import (
	"context"
	"strings"
	"time"

	rt "github.com/arnodel/golua/runtime"
	"github.com/dyskette/atsume/internal/txquery"
)

// registerGoluaBuiltins installs the free functions and constants modules
// expect, the golua counterpart of registerBuiltins. ctx bounds sleep.
func registerGoluaBuiltins(r *rt.Runtime, ctx context.Context) {
	env := r.GlobalEnv()
	for name, v := range map[string]int64{
		"no_error":              noError,
		"net_problem":           netProblem,
		"information_not_found": informationNotFound,
		"asUnknown":             asUnknown,
		"asChecking":            asChecking,
		"asValid":               asValid,
		"asInvalid":             asInvalid,
	} {
		env.Set(rt.StringValue(name), rt.IntValue(v))
	}

	// sleep(milliseconds): see registerBuiltins for why modules need it. A
	// cancelled context ends the whole call rather than just the wait, since
	// the module would otherwise carry on as though it had slept.
	setGoFunc(r, env, "sleep", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		ms, err := checkInt(c, 0)
		if err != nil {
			return nil, err
		}
		d := time.Duration(ms) * time.Millisecond
		if d <= 0 {
			return c.Next(), nil
		}
		d = min(d, maxSleep)
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			t.TerminateContext("sleep: %v", ctx.Err())
		}
		return c.Next(), nil
	}, 1, false)

	setGoFunc(r, env, "CreateTXQuery", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		// The argument is usually HTTP.Document, which is userdata.
		q, err := txquery.ParseString(goluaArgText(c, 0))
		if err != nil {
			return c.PushingNext1(t.Runtime, rt.NilValue), nil
		}
		return c.PushingNext1(t.Runtime, pushGoluaQuery(t.Runtime, q)), nil
	}, 1, false)

	// strFns adapts functions of plain strings, all arguments required.
	strFns := map[string]struct {
		n  int
		fn func(a []string) string
	}{
		"GetBetween": {3, func(a []string) string { return GetBetween(a[0], a[1], a[2]) }},
		"SeparateLeft": {2, func(a []string) string {
			left, _, _ := strings.Cut(a[0], a[1])
			return left
		}},
		"SeparateRight": {2, func(a []string) string {
			if _, right, ok := strings.Cut(a[0], a[1]); ok {
				return right
			}
			return a[0]
		}},
		"Trim":          {1, func(a []string) string { return strings.TrimSpace(a[0]) }},
		"MaybeFillHost": {2, func(a []string) string { return MaybeFillHost(a[0], a[1]) }},
	}
	for name, f := range strFns {
		setGoFunc(r, env, name, func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
			args := make([]string, f.n)
			for i := range args {
				s, err := checkString(c, i)
				if err != nil {
					return nil, err
				}
				args[i] = s
			}
			return c.PushingNext1(t.Runtime, rt.StringValue(f.fn(args))), nil
		}, f.n, false)
	}

	setGoFunc(r, env, "MangaInfoStatusIfPos", func(t *rt.Thread, c *rt.GoCont) (rt.Cont, error) {
		s, err := checkString(c, 0)
		if err != nil {
			return nil, err
		}
		lists := [4]string{defaultOngoing, defaultCompleted, defaultHiatus, defaultDropped}
		for i := range lists {
			if lists[i], err = optString(c, i+1, lists[i]); err != nil {
				return nil, err
			}
		}
		return c.PushingNext1(t.Runtime, rt.StringValue(
			MangaInfoStatusIfPos(s, lists[0], lists[1], lists[2], lists[3]))), nil
	}, 5, false)
}
