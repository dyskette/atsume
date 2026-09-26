# Website modules

atsume does not implement site scraping. It runs the Lua modules from
[FMD2](https://github.com/dazedcat19/FMD2), which cover 600+ sites and are
maintained upstream.

## Why they are fetched, not bundled

FMD2's own units declare `License: GPLv2` with no "or later" clause:

```
baseunits/WebsiteModules.pas:2:   License: GPLv2
baseunits/uData.pas:3:           License: GPLv2
```

The Lua modules carry no per-file licence header, so they inherit the
repository's. **GPL-2.0-only is incompatible with GPL-3.0** — the two cannot be
combined into one distributed work.

atsume is GPL-3.0 and resolves this by never distributing a combined work. It is
an independent program that implements an API; the operator's instance clones
the modules at run time. Running GPL software in a host you wrote does not make
the host derivative, in the same way a shell may be distributed without the
scripts people run in it.

### What this means in practice

- **No module file may be committed to this repository** — not in `testdata/`,
  not as a fixture, not "just one to test against". `.gitignore` blocks the
  paths the tooling uses, but the rule is broader than the file.
- **Nothing derived from the corpus may be committed either.** The extracted
  expression list that `internal/txquery` tests against is generated from a
  checkout at test time, never stored.
- Tests that need modules read `ATSUME_FMD2_DIR`, or clone into an ignored
  directory when `ATSUME_FETCH_CORPUS=1` is set. Without either they skip.

## The Lua runtime

Modules run on [golua](https://github.com/arnodel/golua) v0.3.0, a pure-Go
implementation of Lua 5.5. FMD2 itself builds against Lua 5.4, so this is one
step newer than what the modules are written for; gopher-lua, which atsume used
before, implements Lua 5.1 and needed a patched fork to parse them.

Two consequences of 5.5 matter to modules:

- **Integers and floats are distinct.** A float prints as `2.0`, so every
  number atsume hands to Lua that a module might put in a URL — a page number,
  a count, `URL` on a directory page — is an integer.
- **A `for` loop's control variable is read-only.** Nine modules assign to it
  and fail to compile; see the load rate below.

### The sandbox

Modules are fetched from upstream at run time, so they are treated as untrusted
code. The runtime holds only what they use:

- **Libraries are listed, not taken whole.** `golib` (imports Go packages),
  `runtime` and `debug` are left out, as are `dofile` and `loadfile`. `os`
  keeps its clock functions; `os.remove` and `os.rename` exist but always fail,
  because `utils/nodejs.lua` uses `rename` as an existence check.
- **Files are read-only and confined to the checkout.** `io.open` and
  `io.lines` resolve relative paths against the checkout root, FMD2's working
  directory, through `os.OpenInRoot`, which also refuses symlinks and `..`
  that lead out. A write fails as on a read-only filesystem. The exception is
  `OnAfterImageSaved`: upstream lets it edit the page it is given in place, so
  that one file may be read and written while the hook runs.
- **`require` loads only from the `lua` directory**, as text, or from the
  `fmd.*` libraries. golua's own `require` would follow whatever
  `package.path` names; no module sets it.

### Limits and cancellation

golua cannot be interrupted from another goroutine, so a scrape is bounded two
ways instead:

- **A CPU limit of 10⁹ VM ticks on each call** — loading a file, `Init`, or a
  handler — about seven seconds of pure Lua. Time spent in Go (HTTP waits,
  XPath) costs nothing. The heaviest call across the golden and recorded tests
  uses about 52 thousand ticks; decoding 1 MB of JSON with upstream's pure-Lua
  `utils/json` about 40 million. `TestCPULimitHeadroom` keeps at least 100× and
  10× room above those.
- **The scrape's context.** No call starts once it is cancelled. A request in
  flight is abandoned, and the call ends at the module's next request; a
  `sleep` ends it at once. A call cancelled mid-way always returns an error,
  even if the module caught the termination.

A termination raised inside `pcall` ends only that `pcall`, because golua runs
it in a nested context. Upstream uses `pcall` only around JSON decoding, so in
practice a call still stops at its next request, and a loop that kept catching
it would still stop at the CPU limit.

## How modules are evaluated

FMD2 evaluates its modules' XPath with Pascal's `internettools`, an XQuery 3.1
engine. No comparable engine exists in Go, so `internal/txquery` rewrites each
expression into XPath 1.0 and performs the remainder around the evaluation:

| Construct | Upstream | How atsume handles it |
|---|---|---|
| `json(*).data.items()` | XQuery JSON | JSON parsed into a node tree; becomes `/data/items/*` |
| `authors?*?name` | 3.1 lookup operator | becomes `/authors/*/name` |
| `string-join(seq, ", ")` | XQuery | the host joins the node-set |
| `//p/substring-after(., ":")` | function in step position | hoisted to `substring-after(//p, ":")` |
| `(a, b)` | sequence constructor | folded to a union |
| `css("div#id > h1")` | internettools extension | converted to a location path |
| `upper-case()` / `lower-case()` | XPath 2.0 | expressed with `translate()` |
| `replace()`, `tokenize()` | XPath 2.0 | applied by the host as a regex |

As of the revision this was measured against, **3563 of 3577 call sites (99.61%)
translate and compile**. The remaining 14 sit in 10 modules and affect a single
field each rather than the chapter list.

`make corpus` re-measures this against upstream HEAD and fails below 99%.

### The trap this layer exists to avoid

`antchfx/xpath` *compiles* `//p/substring-after(., ":")` without complaint and
evaluates it to the empty string. Left alone, 117 call sites would have returned
blank fields with no error anywhere. The rewriter exists as much to prevent
silent wrong answers as to prevent parse failures.

One matching parity detail, from `baseunits/XQueryEngineHTML.pas`: `XPathString`
does **not** trim its result, while `XPathStringAll` trims every value and skips
the empty ones. Modules rely on both behaviours.

## JavaScript execution

Several sites encrypt their page lists and ship the decryption routine in the
page. FMD2 embeds Duktape for this; atsume uses [goja](https://github.com/dop251/goja),
exposed under the same `fmd.duktape` name the modules require.

The script comes from a scraped page, so it is untrusted input. goja is a
sandboxed interpreter with no filesystem, network or process access of its own,
and atsume grants exactly two capabilities on top:

- **`require()`**, restricted to `.js` files inside the module checkout. The
  path is rejected if it escapes that directory or does not end in `.js`.
- **`require("crypto")`**, a randomness source backed by `crypto/rand`.
  crypto-js probes `window.crypto`, then `global.crypto`, then this, and throws
  outright if none answers. Serving it here rather than defining a `global`
  object is deliberate: scraped scripts branch on `typeof global` to detect
  Node, and inventing one would push them down a path that cannot work.

Every call is bounded by a 15-second interrupt. goja has no preemption, so
without it a `while (true)` in a scraped page would pin a worker permanently.

### Parity notes

From `baseunits/Duktape.pas`, `ExecJS` returns `duk_safe_to_string` of the
completion value, and modules are written against its specifics:

- `undefined` becomes `""`, but `null` reads back as the string `"null"`.
- An array renders through `Array.prototype.toString` (`a,b`), **not** as JSON.
  Modules that want JSON end their script with an explicit `JSON.stringify`.
- Upstream registers a bare `print()` and has no `console`. atsume provides
  both; supplying `console` is a deliberate improvement, since
  `cryptojs-aes-format.js` calls `console.warn` and would otherwise abort the
  whole script over a warning.

One deliberate divergence: upstream catches a JavaScript error, logs it, and
returns `nil` to Lua. atsume raises instead, so a broken script surfaces as a
failed job rather than a chapter that silently yields zero pages.

## Image descrambling

Some sites serve a page cut into a grid with the tiles permuted, shipping the
permutation alongside it. `fmd.imagepuzzle` reassembles them.

`Create(hor, ver)` returns a puzzle over a grid; `Matrix[i]` gives the
destination index of source tile `i`; `DeScramble(input, output)` rewrites the
image. Modules call it as `DeScramble(HTTP.Document, HTTP.Document)`, so
`HTTP.Document` is a mutable stream rather than a string.

Output format follows upstream: PNG in or WebP in yields PNG out, anything else
yields JPEG. WebP is decode-only in Go, so converting it is a necessity rather
than a choice. Descrambling a JPEG costs one generation of re-encoding loss
because the tiles have to move in pixel space; atsume encodes at quality 95,
where upstream leaves it at the toolkit default.

Tile copies are clipped at the image edge, and any area no tile covers is left
opaque white, matching upstream. An image whose dimensions are not a multiple of
the grid leaves such a margin.

### The image hooks

Descrambling happens inside `OnDownloadImage`, so it only works if the download
pipeline runs the module's image hooks rather than fetching pages itself:

| Hook | Modules | What it does |
|---|---|---|
| `OnBeforeDownloadImage` | 113 | sets request headers, almost always a Referer the image host demands |
| `OnDownloadImage` | 12 | the module fetches and transforms the image itself |

Because a module may re-encode an image, the file extension inside the CBZ is
taken from the returned bytes rather than the URL — descrambling a WebP
produces a PNG, and trusting the URL there would misname it.

### Reach

Seven modules use `fmd.imagepuzzle` — Comix, MangaGo, NexusScanlation,
PhiliaScans, PlusComico, TonarinoYoungJump and WolfManga — and all of them
load.

## Unimplemented capabilities

These raise a named error rather than failing quietly:

| Library | Modules affected | Needs |
|---|---|---|
| `utils.nodejs` | Comix, RaijinScans, and a fallback in the Madara template | Puppeteer |

## Fixing a module

When a site changes and its module stops working, `atsume module` runs that
one module from any FMD2 checkout through the same host atsume uses, without
starting atsume:

```sh
atsume module -fmd2 ~/src/FMD2 LeerCapitulo list 1                 # titles on page 1, and the page count
atsume module -fmd2 ~/src/FMD2 LeerCapitulo info /manga/xyz/one/   # every MANGAINFO field
atsume module -fmd2 ~/src/FMD2 LeerCapitulo pages /leer/xyz/one/1/ -fetch-first
atsume module xpath https://www.leercapitulo.co/manga/ '//a[contains(@class,"lc-card-name")]'
```

`info` and `list` show which fields came back empty; `xpath` tries a
replacement selector against the live page with the XPath engine modules run
on, so a selector that works there works in the module. Edit the `.lua` file
and run the first command again. To run atsume itself on a fixed module before
upstream merges it, point `ATSUME_MODULES_REPO` at the checkout and
`ATSUME_MODULES_REF` at its branch; for the tests, `ATSUME_FMD2_DIR`.

## Module load rate

`TestLoadAllModules` opens every upstream module, which runs its `Init()` and so
exercises the whole declaration-time binding surface.

**611 of 621 load (98.4%).** The remainder:

| Cause | Count |
|---|---|
| Assigns to a `for` loop's control variable, which Lua 5.5 forbids | 9 |
| `require 'pb'` — protobuf, which atsume does not implement | 1 |

The nine are LeerCapitulo (`modules/LeerCapitulo.lua:179`) and the eight
modules built on the GroupLe template (`templates/GroupLe.lua:127`). Both are
one-line fixes upstream — a local copy of the variable at the top of the loop
body — that behave the same under 5.4. `TestLoadAllModules` pins the list, so it fails when
FMD2 fixes them and the list needs to shrink.

MangaPlus parses, and then asks for a protobuf implementation that FMD2 links
in from Pascal. Recovering it means a `pb` binding over a Go protobuf library,
which is a separate piece of work.

### Bugs this test has caught

Worth keeping, because each one was invisible from reading the docs:

- `AddOptionCheckBox` is `(name, caption, default)` dot-called, and the setter is
  named `AddOptionEdit`, not `AddOptionEditBox` as `LUA-REFERENCE.md` states.
  Fixing the signature recovered **57 modules** in one change.
- Several modules are saved with a UTF-8 BOM, which a Lua compiler rejects as
  an invalid token on line 1 unless the file goes through the loader that
  skips it, as Lua's own `luaL_loadfile` does. Loading them that way recovered
  **17 more**.
- Three modules seed `MODULE.Storage` from inside `Init()`, so the declaration
  table has to expose the same map the runner reads later.
