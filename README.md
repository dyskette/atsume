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
                              ^                    |
                              +--- every 6h, new chapters only
```

## Status

**606 of 621 upstream modules load** (97.6%). Working: module loading, the
TXQuery/XPath translation layer, JavaScript execution for protected pages,
image descrambling, scheduled subscription checks, the job queue, image
downloads, CBZ output, and a web UI with live progress over SSE.

atsume carries two patches against gopher-lua; see [docs/UPSTREAM.md](docs/UPSTREAM.md).
Without it, 255 of 621 modules fail at handler time.

Not implemented yet: Puppeteer-backed modules and the MangaFox watermark
remover. Modules needing those fail loudly and name the missing capability
rather than returning blank fields.

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
| `ATSUME_CHECK_INTERVAL` | `6h` | How often a subscribed series is re-checked; `0` disables it |
| `ATSUME_CHECK_BATCH` | `10` | Series enqueued per sweep, so a large library spreads out |
| `ATSUME_AUTO_DOWNLOAD` | `true` | Queue newly found chapters automatically |
| `ATSUME_WORKERS` | `3` | Chapters downloaded concurrently |
| `ATSUME_HOST_CONCURRENCY` | `2` | Simultaneous requests per site |
| `ATSUME_HOST_RPS` | `1` | Requests per second per site |
| `ATSUME_FLARESOLVERR_URL` | — | FlareSolverr address, used only after a request is refused by an interstitial |
| `ATSUME_NOTIFY_URL` | — | Receives a JSON POST when a check finds new chapters |
| `ATSUME_SECRET_KEY` | — | Encrypts stored module credentials; without it, storing a login is refused |
| `ATSUME_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

Pin `ATSUME_MODULES_REF` to a commit SHA in production. `master` tracks upstream
and a bad commit there would otherwise reach you unreviewed.

### Subscriptions

Tracking a series records its chapters but downloads nothing: a first import is
a backlog, not news. From then on each check downloads only what appeared since
the last one. Use **Download all pending** on the series page to fetch a
backlog deliberately, and the toggle on that page to stop checking a series
without untracking it.

### Module settings

Each site has a settings page reachable from its listing. Modules declare their
own options — show paid chapters, preferred language, a delay — and the form is
generated from those declarations rather than written per module.

The 28 modules that implement a login take a username and password there. The
password is encrypted with a key derived from `ATSUME_SECRET_KEY`; without a key
set, storing one is refused rather than written in the clear. A failed login
fails the scrape, because a module that needed an account and did not get one
returns a teaser page that would otherwise be recorded as the real chapter list.

### Notifications

`ATSUME_NOTIFY_URL` receives a JSON POST when a check finds new chapters:

```json
{"event":"new_chapters","series":"…","module":"…","count":2,
 "chapters":["Chapter 41","Chapter 42"],"message":"…: 2 new chapter(s)"}
```

The shape is plain JSON rather than any one service's format — ntfy, Gotify,
Apprise and a Discord webhook all differ, and guessing which from the URL would
break on self-hosted instances.

### Anti-bot challenges

With `ATSUME_FLARESOLVERR_URL` set, a request refused by a Cloudflare or
DDoS-Guard interstitial is retried through FlareSolverr, and the clearance
cookie and user agent it earns are carried forward. It is consulted only after a
refusal that actually looks like a challenge, so an ordinary 403 never costs a
browser run.

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
