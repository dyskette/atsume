package scraper

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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

// Use activates an existing checkout.
func (r *Registry) Use(dir, ref string) error {
	mods, err := discover(filepath.Join(dir, "lua", "modules"))
	if err != nil {
		return err
	}
	if len(mods) == 0 {
		return fmt.Errorf("no modules found under %s", dir)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.current, r.ref, r.modules = dir, ref, mods
	return nil
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
