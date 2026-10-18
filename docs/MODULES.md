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

JSON is read as leniently as `json()` reads it upstream: a trailing comma
before `]` or `}` is accepted, because internettools gives `json()` a parser
configured with `jpoAllowTrailingComma`. Sites that embed a JavaScript object
literal as page data depend on it — the MultiChan template's chapters had no
pages without it. Upstream's `parse-json()` is stricter and rejects the same
input; atsume parses both the same way, so there it is the more lenient of the
two. That can turn an upstream failure into a result, never change the reading
of valid JSON.

### In the Rust port

`crates/atsume-txquery` evaluates expressions with
[xee](https://github.com/Paligo/xee), an XPath 3.1 engine, so nothing is
hoisted or folded: sequences, functions in step position and `replace()` are
evaluated as written. What remains of internettools' dialect is rewritten
token by token into standard XPath: dots and calls on JSON values, `?`
lookups, `jn:keys`, `jn:members`, `css()` and bare `true`/`false`/`null`.
`json(…)` is evaluated first and its tree bound to a variable. As of the
revision measured, 3602 of 3603 call sites compile; the corpus test in
`tests/corpus.rs` fails below 99%.

Three readings follow upstream where it is easy to get wrong:

- **Strings compare ignoring ASCII case**, as upstream's default collation
  does: `contains(., "ongoing")` matches `Ongoing`, and `@class = "info"`
  matches `Info`. xee hard-codes its default collation, so atsume takes xee
  from a fork that makes it configurable, and sets xee's
  `html-ascii-case-insensitive`; the fork is pinned in the root `Cargo.toml`.
  Upstream also orders digits by value, putting `a9` before `a10`; atsume
  orders strings as plain text, and no module expression orders strings.

- **`json(*)` reads the parsed page's text**, as upstream does, so markup
  inside a JSON string is dropped and entities are decoded. The modules that
  pass `HTMLEncode(HTTP.Document.ToString())` to `CreateTXQuery` rely on that
  decoding. When the text is not JSON, as when a `<!--` inside a string
  swallows the rest of the page, the response as received is tried; that can
  only turn an upstream failure into a result.
- **HTML names match in any case.** html5ever stores them in lowercase, and
  JSON keys keep theirs, so a name test written with a capital, such as
  `//TotalPage` or `@data-URL`, matches the name as written or in lowercase. A
  JSON object holding both `Name` and `name` would match both for `Name`.
  Generated name tests use `codepoint-equal`, so JSON keys never match
  ignoring case.

#### Comparing with xidel

`tests/xidel.rs` runs expressions on two pages written for it, a series page
and a JSON response, in both atsume and
[xidel](https://github.com/benibela/xidel), internettools' command-line front
end, and requires the same items. FMD2 builds against internettools' latest
revision with no pin, and xidel's last release is from 2018, so CI uses a
development build, pinned by name and SHA-256 in `.github/workflows/ci.yml`.
xidel changes three of the settings the engine FMD2 creates has; the test's
XQuery prolog restores them: `true`, `false` and `null` as literals, path
steps on JSON objects, and string literals without entities.

An expression that still differs is pinned: a JSON `false` used as a
predicate, which is an element here and so true.
To run it locally, point `ATSUME_XIDEL` at a xidel binary and run
`make rust-xidel`.

`tests/internettools.rs` ports cases from internettools' own XPath tests,
which are GPL-3.0-or-later and so may be copied. Only cases for what the
modules use are kept: `json()`, the string functions they call, sequences and
`css()`. Cases left out are those that depend on internettools parsing its
test pages as XML, where FMD2 and atsume parse HTML, and those for features no
module uses, such as `:nth-child(2n+1)` or namespaced selectors.

`crates/atsume-scraper/src/query.rs` gives modules `CreateTXQuery`, following
`baseunits/lua/LuaXQuery.pas` and `LuaIXQValue.pas` rather than the Go port:

- **A failed expression reads as nothing.** Upstream wraps every evaluation in
  `try … except end`. atsume does the same and logs the error at debug level,
  which is where to look when a field comes back blank.
- **`XPathString` is the whole result's text**, every item joined with nothing
  between, untrimmed. `XPathStringAll` trims each value and skips the empty
  ones, except when it fills a list: then it adds every trimmed value. Trimming
  is Pascal's: spaces and control characters only.
- **`XPathCount` counts every item**, not only nodes.
- **`XPath` returns one kind of object for any result**, with `Count`, `Get`,
  `GetAttribute`, `GetProperty` and `ToString`. `Get(i)` out of range is an
  empty object, not `nil`. There is no `#` and no `[i]`, as upstream has none.
- **A context argument stands for its first item**, read on the page it came
  from. A page and every object read from it share one tree, so `ParseHTML`
  parses the new page beside the old one and earlier objects keep working.

Two Go behaviours stay because modules gain from them: a link with no text is
named by its `title` in `XPathHREFAll`, and `XPathStringAll` takes a context
as its second argument.

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

### In the Rust port

The Rust runtime runs scripts in [QuickJS](https://github.com/quickjs-ng/quickjs)
through the `rquickjs` crate, and keeps every rule above. Each `ExecJS` call
gets a fresh runtime, in sloppy mode, because page scripts assign undeclared
variables. An interrupt handler stops a script after 15 seconds, and a script
cannot catch that interrupt. The runtime may allocate no more than the Lua
sandbox's memory limit.

`require` is written in JavaScript, around a Rust function that checks a name
and returns the file. QuickJS frees a runtime only once nothing holds its
objects, and it cannot see an object that a Rust closure holds, so a cache of
loaded helpers kept in Rust would keep every runtime alive. `require` resolves
helpers with the same rule that `io` uses, so a symbolic link that leads out of
the `lua` directory is refused too.

`getRandomValues` fills at most 65,536 bytes per call, the quota that browsers
enforce, so a `Uint32Array` takes at most 16,384 elements. A typed array is
measured by its `byteLength`, and a plain object with a `length` counts four
bytes per element, because each element receives a 32-bit number.
`randomBytes` returns at most 1 MiB. Both loops run in Rust, where the
interrupt cannot stop them, and the limits bound them.

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

atsume's `Create` refuses a grid of more than 2^20 tiles. Real sites
cut a page into a few dozen, and the limit keeps a mistaken size from
allocating a permutation that exhausts memory.

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

Modules such as Comix, MangaGo and PlusComico use `fmd.imagepuzzle`. Which
modules use it changes as FMD2 adds and drops sites, so this document keeps no
list; searching an FMD2 checkout for `fmd.imagepuzzle` gives the current one.

## Watermark removal

MangaFox stamps a banner across the bottom of its pages, below the artwork.
`fmd.mangafoxwatermark` cuts it off. A module calls `LoadTemplate(directory)`
in `Init`, which reads upstream's images of the banner and returns how many it
read, and `RemoveWatermark(FILENAME, asPNG)` in `OnAfterImageSaved`.

atsume reimplements the behaviour from its observable contract rather than
translating upstream's code. The templates and the bottom strip of the page are
reduced to black and white by Otsu's method and compared by peak
signal-to-noise ratio. A strip counts as the banner when it scores at least
9 dB, upstream's value, and its top four rows are white, because a white gap
separates the banner from the artwork. The banner's height is then cut off the
page. A page with no banner, or one that matches over its whole height, is left
untouched.

A PNG stays a PNG and anything else is saved as a JPEG at quality 95. With
`asPNG` set, the page is saved as a `.png` beside the original, which is
deleted, so the download pipeline reads back whatever file the hook left.

`RemoveWatermark` may change only the page that `OnAfterImageSaved` is editing,
the same file that `io.open` may write. A directory's templates are read once
for the life of the process, because `Init` runs for every scrape. A directory
that cannot be read gives 0, as upstream does, and a warning is logged, because
otherwise the failure looks like a page without a banner. atsume also reads
templates only from inside the checkout, as its `io` does.

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
replacement selector with the XPath engine modules run on, so a selector that
works there works in the module. `fetch` saves a page as atsume's HTTP client
gets it, and `xpath` reads a saved page too, so selectors can be tried without
asking the site each time:

```sh
atsume module fetch https://www.leercapitulo.co/manga/ -o list.html
atsume module xpath list.html '//a[contains(@class,"lc-card-name")]/@href'
```

Edit the `.lua` file and run the first command again. Once it works, record
it, so the next change to the site fails a test instead of a read:

```sh
atsume module -fmd2 ~/src/FMD2 LeerCapitulo record /manga/xyz/one/ /leer/xyz/one/1/
```

That writes `internal/scraper/testdata/recorded/leercapitulo/`: `case.json`,
which is committed, and the recorded responses and expected result, which are
the site's content and stay local. To run atsume itself on a fixed module before
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
