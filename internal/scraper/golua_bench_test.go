package scraper

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// BenchmarkScrape runs a whole scrape — open the module, list the directory
// where the case has one, read the series, read a chapter — against the
// golden fixtures of the two templates that back the most modules, under
// each runtime. Every scrape opens a fresh runner, as the worker pool does.
//
//	ATSUME_FMD2_DIR=... go test ./internal/scraper/ -run '^$' -bench Scrape -benchmem
func BenchmarkScrape(b *testing.B) {
	benchmarkGolden(b, func(b *testing.B, r scrapeRunner, c goldenCase, base string) {
		if c.directory {
			if _, err := r.GetNameAndLink(0); err != nil {
				b.Fatal(err)
			}
		}
		if _, err := r.GetInfo(base + c.seriesPath); err != nil {
			b.Fatal(err)
		}
		if _, err := r.GetPageNumber(base + c.chapterPath); err != nil {
			b.Fatal(err)
		}
	})
}

// BenchmarkOpen measures loading alone: running the module file, the
// template it requires, and Init.
func BenchmarkOpen(b *testing.B) {
	benchmarkGolden(b, func(*testing.B, scrapeRunner, goldenCase, string) {})
}

func benchmarkGolden(b *testing.B, scrape func(b *testing.B, r scrapeRunner, c goldenCase, base string)) {
	dir := os.Getenv("ATSUME_FMD2_DIR")
	if dir == "" {
		b.Skip("set ATSUME_FMD2_DIR to an FMD2 checkout")
	}
	luaRoot := filepath.Join(dir, "lua")
	for _, c := range goldenCases {
		if c.name != "madara" && c.name != "mangathemesia" {
			continue
		}
		for _, lr := range luaRuntimes {
			b.Run(c.name+"/"+lr.name, func(b *testing.B) {
				caseDir := filepath.Join("testdata", "golden", c.name)
				_, base := goldenServer(b, caseDir, c.routes)
				modPath := filepath.Join(b.TempDir(), c.name+".lua")
				if err := os.WriteFile(modPath, []byte(fmt.Sprintf(c.module, base)), 0o644); err != nil {
					b.Fatal(err)
				}
				h := &Host{LuaDir: luaRoot, Transport: http.DefaultTransport}
				b.ReportAllocs()
				for b.Loop() {
					r, err := lr.open(h, context.Background(), modPath, "", "")
					if err != nil {
						b.Fatal(err)
					}
					scrape(b, r, c, base)
					r.Close()
				}
			})
		}
	}
}
