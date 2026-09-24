package scraper

import (
	"context"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// minLoadRate is the share of upstream modules that must load. It guards
// against a regression in the Lua host. The known shortfall is MangaPlus,
// which needs a C protobuf library, and under golua the nine modules Lua 5.5
// rejects for assigning to a for loop's control variable.
const minLoadRate = 0.97

// TestLoadAllModules opens every upstream module and reports which fail.
//
// Loading runs each module's Init(), so this exercises the whole binding
// surface a module touches at declaration time: NewWebsiteModule, the option
// setters, and any require() of a shared template at file scope.
func TestLoadAllModules(t *testing.T) {
	dir := luaDir(t)
	reg := NewRegistry("", "")
	if err := reg.Use(filepath.Dir(dir), "test"); err != nil {
		t.Fatal(err)
	}

	mods := reg.Modules()
	if len(mods) < 500 {
		t.Fatalf("only %d modules discovered; the checkout looks wrong", len(mods))
	}

	eachRuntime(t, func(t *testing.T, lr luaRuntime) {
		h := &Host{LuaDir: dir}
		ctx := context.Background()

		failures := map[string]string{}
		for _, m := range mods {
			r, err := lr.open(h, ctx, m.File, "", "")
			if err != nil {
				failures[m.Name] = err.Error()
				continue
			}
			if r.Module().Name == "" {
				failures[m.Name] = "Init() declared no Name"
			}
			r.Close()
		}

		loaded := len(mods) - len(failures)
		rate := float64(loaded) / float64(len(mods))
		t.Logf("%s: %d/%d modules loaded (%.2f%%)", lr.name, loaded, len(mods), rate*100)

		// Group the failures so a regression is legible rather than a wall of text.
		byCause := map[string][]string{}
		for name, err := range failures {
			byCause[classifyLoadError(err)] = append(byCause[classifyLoadError(err)], name)
		}
		causes := make([]string, 0, len(byCause))
		for c := range byCause {
			causes = append(causes, c)
		}
		sort.Slice(causes, func(i, j int) bool { return len(byCause[causes[i]]) > len(byCause[causes[j]]) })
		for _, c := range causes {
			sort.Strings(byCause[c])
			t.Logf("  %-24s %2d  %s", c, len(byCause[c]), strings.Join(byCause[c], ", "))
		}

		// Always print the detail for causes other than the known Lua 5.3 gap: a new
		// failure there is a binding bug worth seeing without having to breach the
		// floor first.
		for _, c := range causes {
			if c == "lua 5.3 operators" || c == "lua 5.5 read-only for" {
				continue
			}
			for _, name := range byCause[c] {
				t.Logf("    %s: %s", name, firstLine(failures[name]))
			}
		}

		if rate < minLoadRate {
			t.Errorf("load rate %.2f%% below the %.2f%% floor", rate*100, minLoadRate*100)
		}
	})
}

// firstLine trims a Lua stack traceback down to its message.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// classifyLoadError buckets a load failure by its root cause.
func classifyLoadError(err string) string {
	switch {
	// Lua 5.5 makes a for loop's control variable read-only; nine upstream
	// modules assign to it. Only golua reports this.
	case strings.Contains(err, "constant variable"):
		return "lua 5.5 read-only for"
	case strings.Contains(err, "goto"):
		return "lua 5.2 goto"
	// Bitwise operators and floor division are Lua 5.3 syntax that gopher-lua's
	// 5.1 parser rejects outright.
	case strings.Contains(err, `near '&'`) || strings.Contains(err, `near '|'`) ||
		strings.Contains(err, `near '~'`) || strings.Contains(err, `near '<<'`) ||
		strings.Contains(err, `near '>>'`) || strings.Contains(err, `near '/'`):
		return "lua 5.3 operators"
	case strings.Contains(err, "module") && strings.Contains(err, "not found"):
		return "missing require target"
	case strings.Contains(err, "Init never called"):
		return "no NewWebsiteModule call"
	case strings.Contains(err, "parse error") || strings.Contains(err, "syntax error"):
		return "other parse error"
	default:
		return "runtime error in Init"
	}
}
