package scraper

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Registry manages the on-disk checkout of upstream website modules.
//
// The modules are GPL-2.0-only, which is incompatible with this program's
// GPL-3.0 licence, so they are never vendored or shipped. The operator's
// instance fetches them at runtime and the two works stay separate. A checkout
// is pinned to a revision and replaced wholesale, keeping a previous revision
// on disk so a bad upstream commit can be rolled back.
type Registry struct {
	// Root is the directory holding one subdirectory per fetched revision.
	Root string
	// Repo is the upstream git URL.
	Repo string

	mu      sync.RWMutex
	current string // absolute path of the active checkout
	ref     string // the revision that checkout is at
	modules []ModuleInfo
	commit  string    // the commit checked out, "" outside git
	date    time.Time // when that commit was made
}

// ModuleInfo is a module discovered on disk, before it is loaded into Lua.
type ModuleInfo struct {
	// Name is the file's base name, which is also how FMD2 refers to a module.
	Name string
	File string
}

// NewRegistry returns a registry rooted at dir.
func NewRegistry(dir, repo string) *Registry {
	return &Registry{Root: dir, Repo: repo}
}

// Fetch clones the repository at ref into the registry root and makes it
// current. An existing checkout for the same ref is reused.
func (r *Registry) Fetch(ctx context.Context, ref string) error {
	dest := filepath.Join(r.Root, sanitizeRef(ref))

	if _, err := os.Stat(filepath.Join(dest, "lua")); err != nil {
		if err := os.MkdirAll(r.Root, 0o755); err != nil {
			return err
		}
		tmp := dest + ".partial"
		os.RemoveAll(tmp)

		// A shallow clone of one ref is all that is needed and keeps the
		// download to a few megabytes rather than the full history.
		cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1",
			"--branch", ref, r.Repo, tmp)
		if out, err := cmd.CombinedOutput(); err != nil {
			os.RemoveAll(tmp)
			return fmt.Errorf("clone %s at %s: %w: %s", r.Repo, ref, err, strings.TrimSpace(string(out)))
		}
		if err := os.Rename(tmp, dest); err != nil {
			os.RemoveAll(tmp)
			return err
		}
	}
	return r.Use(dest, ref)
}

// Update replaces the checkout of the current ref with its latest commit and
// makes it current, keeping the one it replaces beside it as .old. A checkout
// is otherwise never refreshed: "master" means master on the day of the
// first run until someone asks. It reports whether there was anything newer;
// asking the repository first means an up-to-date checkout costs no clone.
func (r *Registry) Update(ctx context.Context) (bool, error) {
	if latest := r.latest(ctx); latest != "" && latest == r.Commit() {
		return false, nil
	}
	before := r.Commit()
	if err := r.replace(ctx); err != nil {
		return false, err
	}
	return r.Commit() != before, nil
}

// latest asks the repository which commit the current ref is at, "" when it
// cannot say.
func (r *Registry) latest(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "git", "ls-remote", r.Repo, r.Ref()).Output()
	if err != nil {
		return ""
	}
	hash, _, _ := strings.Cut(string(out), "\t")
	return strings.TrimSpace(hash)
}

func (r *Registry) replace(ctx context.Context) error {
	ref := r.Ref()
	dest := filepath.Join(r.Root, sanitizeRef(ref))
	tmp, old := dest+".new", dest+".old"
	os.RemoveAll(tmp)
	cmd := exec.CommandContext(ctx, "git", "clone", "--depth", "1", "--branch", ref, r.Repo, tmp)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmp)
		return fmt.Errorf("clone %s at %s: %w: %s", r.Repo, ref, err, strings.TrimSpace(string(out)))
	}
	if _, err := discover(filepath.Join(tmp, "lua", "modules")); err != nil {
		os.RemoveAll(tmp)
		return err
	}
	os.RemoveAll(old)
	if err := os.Rename(dest, old); err != nil && !os.IsNotExist(err) {
		os.RemoveAll(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Rename(old, dest)
		return err
	}
	return r.Use(dest, ref)
}

// Use activates an existing checkout.
func (r *Registry) Use(dir, ref string) error {
	mods, err := discover(filepath.Join(dir, "lua", "modules"))
	if err != nil {
		return err
	}
	if len(mods) == 0 {
		return fmt.Errorf("no modules found under %s", dir)
	}
	commit, date := lastCommit(dir)

	r.mu.Lock()
	defer r.mu.Unlock()
	r.current, r.ref, r.modules = dir, ref, mods
	r.commit, r.date = commit, date
	return nil
}

// lastCommit reads the commit a checkout is at and when it was made, empty
// for a directory that is not a git checkout.
func lastCommit(dir string) (string, time.Time) {
	out, err := exec.Command("git", "-C", dir, "log", "-1", "--format=%H %cI").Output()
	if err != nil {
		return "", time.Time{}
	}
	hash, when, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	t, _ := time.Parse(time.RFC3339, when)
	return hash, t
}

// Commit returns the commit the active checkout is at, "" outside git.
func (r *Registry) Commit() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.commit
}

// CommitDate returns when the active checkout's commit was made.
func (r *Registry) CommitDate() time.Time {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.date
}

// LuaDir returns the lua directory of the active checkout.
func (r *Registry) LuaDir() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.current == "" {
		return ""
	}
	return filepath.Join(r.current, "lua")
}

// Ref returns the active revision.
func (r *Registry) Ref() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.ref
}

// Modules lists the discovered modules.
func (r *Registry) Modules() []ModuleInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return append([]ModuleInfo(nil), r.modules...)
}

// Find returns a module by its file name, case-insensitively.
func (r *Registry) Find(name string) (ModuleInfo, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, m := range r.modules {
		if strings.EqualFold(m.Name, name) {
			return m, true
		}
	}
	return ModuleInfo{}, false
}

// Host returns a Host bound to the active checkout.
func (r *Registry) Host(limiter Limiter) *Host {
	return &Host{LuaDir: r.LuaDir(), Limiter: limiter}
}

// HostWith returns a Host bound to the active checkout using a specific
// transport and anti-bot solver.
func (r *Registry) HostWith(limiter Limiter, transport http.RoundTripper, solver *Flaresolverr) *Host {
	return &Host{LuaDir: r.LuaDir(), Limiter: limiter, Transport: transport, Solver: solver}
}

func discover(dir string) ([]ModuleInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []ModuleInfo
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".lua" {
			continue
		}
		out = append(out, ModuleInfo{
			Name: strings.TrimSuffix(e.Name(), ".lua"),
			File: filepath.Join(dir, e.Name()),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// sanitizeRef makes a git ref safe as a directory name.
func sanitizeRef(ref string) string {
	return strings.NewReplacer("/", "-", "\\", "-", "..", "-").Replace(ref)
}
