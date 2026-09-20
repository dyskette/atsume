package txquery

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/antchfx/xpath"
)

// minParseRate is the share of FMD2 call sites that must still translate to
// XPath 1.0. It guards against upstream adopting XQuery constructs the rewriter
// does not handle. Raise it when the rewriter improves; never lower it silently.
const minParseRate = 0.99

// upstreamRepo is cloned on demand. Its modules are GPL-2.0-only and are never
// committed to this repository — see .gitignore.
const upstreamRepo = "https://github.com/dazedcat19/FMD2.git"

// corpusDir returns a checkout of the FMD2 lua tree, cloning one if allowed.
func corpusDir(t *testing.T) string {
	t.Helper()
	if dir := os.Getenv("ATSUME_FMD2_DIR"); dir != "" {
		return dir
	}
	dir := filepath.Join("testdata", "fmd2")
	if _, err := os.Stat(filepath.Join(dir, "lua")); err == nil {
		return dir
	}
	if os.Getenv("ATSUME_FETCH_CORPUS") == "" {
		t.Skip("no FMD2 checkout; set ATSUME_FETCH_CORPUS=1 to clone, or ATSUME_FMD2_DIR to point at one")
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("git", "clone", "--depth", "1", upstreamRepo, dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("clone: %v\n%s", err, out)
	}
	return dir
}

// TestCorpusParseRate is the upstream drift check: every XPath expression in
// every FMD2 module must survive Rewrite and compile.
func TestCorpusParseRate(t *testing.T) {
	dir := corpusDir(t)
	exprs, err := ExtractExpressions(os.DirFS(dir), "lua")
	if err != nil {
		t.Fatal(err)
	}
	if len(exprs) < 1000 {
		t.Fatalf("only %d expressions extracted; the checkout looks wrong", len(exprs))
	}

	var ok int
	failures := map[string][]string{}
	caps := map[string]int{}
	for _, e := range exprs {
		out, c := Rewrite(e.Expr)
		if compiles(out) {
			ok++
			for _, n := range c.names() {
				caps[n]++
			}
			continue
		}
		failures[e.File] = append(failures[e.File], e.Expr)
	}

	rate := float64(ok) / float64(len(exprs))
	t.Logf("%d/%d call sites compile (%.2f%%) across %d modules",
		ok, len(exprs), rate*100, len(failures))

	var capNames []string
	for k := range caps {
		capNames = append(capNames, k)
	}
	sort.Slice(capNames, func(i, j int) bool { return caps[capNames[i]] > caps[capNames[j]] })
	for _, k := range capNames {
		t.Logf("  host capability %-12s %5d", k, caps[k])
	}

	if rate < minParseRate {
		var files []string
		for f := range failures {
			files = append(files, f)
		}
		sort.Strings(files)
		for _, f := range files {
			for _, e := range failures[f] {
				t.Errorf("%s: %s", f, e)
			}
		}
		t.Fatalf("parse rate %.2f%% below the %.2f%% floor", rate*100, minParseRate*100)
	}
}

// compiles reports whether the rewritten expression is valid XPath 1.0.
func compiles(expr string) (ok bool) {
	defer func() {
		if recover() != nil {
			ok = false
		}
	}()
	_, err := xpath.Compile(expr)
	return err == nil
}
