# atsume

集め — *gathering*. A manga downloader that runs the website modules from
[FMD2](https://github.com/dazedcat19/FMD2).

FMD2 maintains Lua modules for 600+ manga sites and keeps them working. atsume
is a small Go host for those modules: it runs them, downloads what they find,
and writes CBZ files into a directory a library server such as Komga scans. It
deliberately does not include a reader or a library model.

```
   browse a site  ->  track a title  ->  chapters queued  ->  CBZ in your library
        Lua module         SQLite            worker pool          Komga scans it
```

## Status

**606 of 621 upstream modules load** (97.6%). Working: module loading, the
TXQuery/XPath translation layer, JavaScript execution for protected pages, the
job queue, image downloads, CBZ output, and a web UI with live progress over
SSE.

Not implemented yet: Puppeteer-backed modules, the MangaFox watermark remover,
and anti-bot solving. Modules needing those fail loudly and name the missing
capability rather than returning blank fields.

The 15 modules that do not load break down as 14 written against Lua 5.3 syntax
(bitwise operators, floor division) that gopher-lua's 5.1 parser rejects, plus
FanFox, which needs the MangaFox watermark remover at declaration time.

## Running

```sh
podman run -d --name atsume \
  -p 8080:8080 \
  -v atsume-data:/data \
  -v /srv/manga:/library \
  ghcr.io/dyskette/atsume:latest
```

On first start it clones the module repository into `/data/modules` and reports
how many modules loaded.

### Configuration

Every variable also accepts a `_FILE` suffix naming a file to read the value
from, so container secrets work without atsume knowing about any particular
secret store.

| Variable | Default | Meaning |
|---|---|---|
| `ATSUME_ADDR` | `:8080` | Listen address |
| `ATSUME_DATA_DIR` | `/data` | Database and module checkouts |
| `ATSUME_LIBRARY_DIR` | `/library` | Where CBZ files are written |
| `ATSUME_MODULES_REPO` | FMD2 on GitHub | Module source repository |
| `ATSUME_MODULES_REF` | `master` | Pinned revision |
| `ATSUME_WORKERS` | `3` | Chapters downloaded concurrently |
| `ATSUME_HOST_CONCURRENCY` | `2` | Simultaneous requests per site |
| `ATSUME_HOST_RPS` | `1` | Requests per second per site |
| `ATSUME_FLARESOLVERR_URL` | — | Anti-bot solver, if you run one |
| `ATSUME_SECRET_KEY` | — | Encrypts stored module credentials |
| `ATSUME_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Pin `ATSUME_MODULES_REF` to a commit SHA in production. `master` tracks upstream
and a bad commit there would otherwise reach you unreviewed.

### Output layout

```
/library/Solo Leveling/Solo Leveling - c001.cbz
/library/Solo Leveling/Solo Leveling - c015 (v02).cbz
```

This is the naming Komga parses. It is fixed rather than configurable because
getting it wrong produces a library that looks populated but sorts into
nonsense.

## Licensing

atsume is GPL-3.0. **The website modules are not part of it.** They come from
FMD2, which is GPL-2.0-*only* and therefore incompatible with GPL-3.0. atsume
never bundles, vendors or redistributes them — your instance fetches them at run
time, and the two works stay separate.

This has one consequence for contributors: no module file, and nothing derived
from one, may be committed to this repository. See [docs/MODULES.md](docs/MODULES.md).

## Development

```sh
make tools     # install templ
make dev       # live reload on :8080
make check     # gofmt, vet, tests, stale-codegen check
make corpus    # upstream drift check against a fresh FMD2 clone
```

`make corpus` reports how much of FMD2's XPath surface still translates to the
XPath 1.0 subset atsume evaluates. It fails below 99%, which is the signal that
upstream has adopted a construct the rewriter does not handle.

`go test ./internal/scraper/ -run TestLoadAllModules -v` opens every upstream
module and groups the failures by cause. It is the fastest way to tell a host
binding bug from an upstream change.
