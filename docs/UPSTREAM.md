# Upstream patches

atsume carries one patch against a dependency. This file records why, how to
apply it, and when it can be dropped.

## gopher-lua: generic-for header miscompiles a chained call

**Status**: reported upstream, patch in `patches/gopher-lua-generic-for.patch`.
Present in v1.1.2 and still on master as of 2026-04-01.

### Symptom

A generic `for` whose header calls a method on the result of another call fails
at runtime with `attempt to call a non-function object`:

```lua
local obj = { Get = makeIter }
local function maker() return obj end

for v in obj.Get() do end      -- works
for v in maker().Get() do end  -- "attempt to call a non-function object"
```

The same expression evaluates correctly outside a `for` header, so
`local it = maker().Get()` yields the iterator as expected.

Verified against two other implementations, both of which return the correct
result: LuaJIT 2.1 and `arnodel/golua`.

### Why it matters here

`x.XPath(expr).Get()` is the standard idiom for iterating a node set in FMD2's
modules. It appears at 160 call sites, in 109 modules directly and in 20 shared
templates — including MangaThemesia, which alone backs 71 modules.

**255 of 621 modules (41%) are affected.** The failure is invisible to a module
load test, because loading only runs `Init()`; it surfaces when a handler runs.

### Cause

`compileGenericForStmt` reserves three registers for the generator, state and
control values, then compiles the expression list directly into them:

```go
rgen := context.RegisterLocalVar("(for generator)")
context.RegisterLocalVar("(for state)")
context.RegisterLocalVar("(for control)")

compileRegAssignment(context, stmt.Names, stmt.Exprs, context.RegTop()-3, 3, sline(stmt))
```

A nested call inside that expression allocates its temporaries over the same
registers, so `TFORLOOP` ends up calling whatever the inner expression left in
the generator slot. The patch compiles into scratch registers above the
reserved slots and moves the results down.

gopher-lua's own test suite passes with the patch applied.

### Applying it

```sh
# once, on your fork of github.com/yuin/gopher-lua
git checkout -b atsume-generic-for v1.1.2
git apply /path/to/atsume/patches/gopher-lua-generic-for.patch
git commit -am "fix: do not clobber generic-for registers with a nested call"
git tag v1.1.2-atsume && git push origin atsume-generic-for --tags
```

Then in `go.mod`:

```
replace github.com/yuin/gopher-lua => github.com/<you>/gopher-lua v1.1.2-atsume
```

`TestRuntimeSupportsChainedForIn` fails while the patch is missing, and is the
canary for this whole class of breakage. Drop the patch, the `replace` and this
section once the fix lands upstream.
