package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/dyskette/atsume/internal/scraper"
	"github.com/dyskette/atsume/internal/txquery"
)

const moduleUsage = `usage:
  atsume module [flags] <Module> list [page]     titles on one directory page, and the page count
  atsume module [flags] <Module> info <url>      every MANGAINFO field for one series
  atsume module [flags] <Module> pages <url>     image URLs for one chapter
  atsume module xpath <url> <expression>         what an XPath expression finds on a live page

<Module> is the file name in lua/modules, without .lua. Series and chapter
URLs are handed to the module as given, the way atsume stores them: usually
relative, like /manga/one-piece/.

flags:
`

// runModule runs one FMD2 module, or one XPath expression, from the command
// line through the same host atsume uses when it runs: what works here works
// there. It is for fixing a module after its site changed, without starting
// atsume or writing a test to reach it.
func runModule(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("module", flag.ContinueOnError)
	fs.SetOutput(out)
	fmd2 := fs.String("fmd2", defaultFMD2(), "FMD2 checkout to load modules from (default $ATSUME_FMD2_DIR, else ./fmd2)")
	site := fs.String("site", "", "the website to run, for a file that declares several")
	root := fs.String("root", "", "an address to read the site at instead of the module's RootURL")
	dir := fs.Int("dir", 0, "the directory (section) to list, from 0")
	fetchFirst := fs.Bool("fetch-first", false, "with pages: download the first image and report what came back")
	fs.Usage = func() {
		fmt.Fprint(out, moduleUsage)
		fs.PrintDefaults()
	}
	// Flags may come anywhere, so "… pages <url> -fetch-first" means what it
	// says; the flag package alone stops at the first argument.
	var rest []string
	for {
		if err := fs.Parse(args); err != nil {
			return err
		}
		if fs.NArg() == 0 {
			break
		}
		rest = append(rest, fs.Arg(0))
		args = fs.Args()[1:]
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	if len(rest) >= 1 && rest[0] == "xpath" {
		if len(rest) != 3 {
			fs.Usage()
			return errors.New("xpath takes a URL and an expression")
		}
		return runXPath(ctx, out, rest[1], rest[2])
	}
	if len(rest) < 2 {
		fs.Usage()
		return errors.New("name a module and an action")
	}

	file := filepath.Join(*fmd2, "lua", "modules", rest[0]+".lua")
	if _, err := os.Stat(file); err != nil {
		return fmt.Errorf("no module file %s; name the file in lua/modules without .lua, and use -site for one of several sites it declares", file)
	}
	host := &scraper.Host{LuaDir: filepath.Join(*fmd2, "lua")}
	r, err := host.Open(ctx, file, *site, *root)
	if err != nil {
		return err
	}
	defer r.Close()

	action, arg := rest[1], ""
	if len(rest) > 2 {
		arg = rest[2]
	}
	switch action {
	case "list":
		return moduleList(out, r, *dir, arg)
	case "info":
		if arg == "" {
			return errors.New("info takes a series URL")
		}
		return moduleInfo(out, r, arg)
	case "pages":
		if arg == "" {
			return errors.New("pages takes a chapter URL")
		}
		return modulePages(ctx, out, r, arg, *fetchFirst)
	}
	fs.Usage()
	return fmt.Errorf("unknown action %q", action)
}

func defaultFMD2() string {
	if d := os.Getenv("ATSUME_FMD2_DIR"); d != "" {
		return d
	}
	return "fmd2"
}

func moduleList(out io.Writer, r *scraper.Runner, dir int, pageArg string) error {
	page := 1
	if pageArg != "" {
		n, err := strconv.Atoi(pageArg)
		if err != nil || n < 1 {
			return fmt.Errorf("page %q: pages count from 1", pageArg)
		}
		page = n
	}
	r.SetDirectoryIndex(dir)
	entries, err := r.GetNameAndLink(page - 1)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s · directory %d of %d · page %d\n", r.Module().Name, dir+1, r.TotalDirectories(), page)
	count := "the module reported no page count"
	if n := r.DirectoryPageNumber(); n > 0 {
		count = fmt.Sprintf("the module reports %d pages", n)
	}
	fmt.Fprintf(out, "%d titles · %s\n\n", len(entries), count)
	for _, e := range entries {
		fmt.Fprintf(out, "%s\t%s\n", e.Link, e.Name)
	}
	return nil
}

func moduleInfo(out io.Writer, r *scraper.Runner, url string) error {
	info, err := r.GetInfo(url)
	if err != nil {
		return err
	}
	for _, f := range []struct{ name, value string }{
		{"Title", info.Title}, {"AltTitles", info.AltTitles}, {"CoverLink", info.CoverLink},
		{"Authors", info.Authors}, {"Artists", info.Artists}, {"Genres", info.Genres},
		{"Status", info.Status}, {"Summary", info.Summary},
	} {
		v := f.value
		if strings.TrimSpace(v) == "" {
			v = "(empty)"
		}
		fmt.Fprintf(out, "%-10s %s\n", f.name, oneLine(v, 160))
	}
	links, names := info.ChapterLinks.All(), info.ChapterNames.All()
	fmt.Fprintf(out, "\n%d chapters", len(links))
	if len(names) != len(links) {
		fmt.Fprintf(out, " (but %d names: the lists should match)", len(names))
	}
	fmt.Fprintln(out)
	for i, l := range links {
		if len(links) > 10 && i == 5 {
			fmt.Fprintf(out, "… %d more …\n", len(links)-10)
		}
		if len(links) > 10 && i >= 5 && i < len(links)-5 {
			continue
		}
		name := ""
		if i < len(names) {
			name = names[i]
		}
		fmt.Fprintf(out, "%s\t%s\n", l, name)
	}
	return nil
}

func modulePages(ctx context.Context, out io.Writer, r *scraper.Runner, url string, fetchFirst bool) error {
	pages, err := r.GetPageNumber(url)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%d pages\n", len(pages))
	for _, p := range pages {
		fmt.Fprintln(out, p)
	}
	if !fetchFirst || len(pages) == 0 {
		return nil
	}
	first := scraper.MaybeFillHost(r.Module().RootURL, pages[0])
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, first, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", scraper.DefaultUserAgent)
	req.Header.Set("Referer", r.Module().RootURL)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("first image: %w", err)
	}
	defer resp.Body.Close()
	n, _ := io.Copy(io.Discard, resp.Body)
	fmt.Fprintf(out, "\nfirst image: HTTP %d · %s · %d bytes\n", resp.StatusCode, resp.Header.Get("Content-Type"), n)
	return nil
}

// runXPath fetches a page and reports what an expression finds on it, with
// the XPath engine modules run on, FMD2's extensions included.
func runXPath(ctx context.Context, out io.Writer, url, expr string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", scraper.DefaultUserAgent)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	q, err := txquery.Parse(resp.Body)
	if err != nil {
		return err
	}
	vals, err := q.Values(expr)
	fmt.Fprintf(out, "HTTP %d · %d results\n", resp.StatusCode, len(vals))
	if err != nil {
		return fmt.Errorf("expression: %w", err)
	}
	for _, v := range vals {
		fmt.Fprintln(out, oneLine(v, 200))
	}
	return nil
}

// oneLine flattens and shortens a value for a terminal line.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
