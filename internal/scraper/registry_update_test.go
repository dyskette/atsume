package scraper

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRegistryUpdate covers updating the modules: a checkout is reused as it
// is until asked, and Update brings in the ref's latest commit.
func TestRegistryUpdate(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	repo := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", repo, "-c", "user.name=t", "-c", "user.email=t@t"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	write := func(name string) {
		t.Helper()
		dir := filepath.Join(repo, "lua", "modules")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte("function Init() end\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("init", "-q", "-b", "master")
	write("One.lua")
	git("add", ".")
	git("commit", "-q", "-m", "one")

	ctx := context.Background()
	reg := NewRegistry(t.TempDir(), repo)
	if err := reg.Fetch(ctx, "master"); err != nil {
		t.Fatal(err)
	}
	first := reg.Commit()
	if first == "" || reg.CommitDate().IsZero() || len(reg.Modules()) != 1 {
		t.Fatalf("after fetch: commit %q, date %v, %d modules", first, reg.CommitDate(), len(reg.Modules()))
	}

	write("Two.lua")
	git("add", ".")
	git("commit", "-q", "-m", "two")

	// Fetching again reuses the checkout; only Update goes to the repository.
	if err := reg.Fetch(ctx, "master"); err != nil {
		t.Fatal(err)
	}
	if reg.Commit() != first {
		t.Error("Fetch replaced an existing checkout")
	}
	changed, err := reg.Update(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !changed || reg.Commit() == first || len(reg.Modules()) != 2 {
		t.Errorf("after update: changed %v, commit %q (was %q), %d modules", changed, reg.Commit(), first, len(reg.Modules()))
	}
	// Nothing newer: it says so without replacing the checkout.
	if changed, err := reg.Update(ctx); err != nil || changed {
		t.Errorf("second update: changed %v, err %v", changed, err)
	}
}
