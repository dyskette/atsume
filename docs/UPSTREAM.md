# Upstream patches

atsume carries two patches against a dependency. This file records why, how to
apply them, and when they can be dropped.

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

## gopher-lua: no Lua 5.3 bitwise operators or floor division

**Status**: not an upstream bug. gopher-lua implements Lua 5.1 deliberately;
this is an extension, in `patches/gopher-lua-lua53-operators.patch`.

### Symptom

Thirteen module files cannot be parsed at all:

```
Comix.lua line:102(column:52) near '&': Invalid token
MangaFire.lua line:62(column:37) near '~': Invalid '~' token
TonarinoYoungJump.lua line:98(column:61) near '/': syntax error
```

They use `&`, `|`, `~` (both binary and unary), `<<`, `>>` and `//`, which
arrived in Lua 5.3. Across the affected files there are 45 uses of `&`, 36 of
`~`, 15 of `>>`, 11 of `<<`, 10 of `|` and 5 of `//` — all of it decrypting
page addresses or decoding UTF-8 by hand.

### Why it matters here

**14 of 621 module files, 16 sites**, including MangaFire, ManHuaGui and
MangaPlus. Unlike the generic-for bug this one is loud: the file does not
load, so atsume lists the site as unavailable rather than producing wrong
results.

### What the patch does

Adds the seven operators to the lexer, the grammar, the compiler and the VM.
New opcodes are **appended** after `OP_NOP` rather than grouped with the other
arithmetic, because the VM dispatch table is positional and inserting would
renumber every opcode after the insertion point.

The change is additive by construction: every one of these tokens is a parse
error in unpatched gopher-lua, so no program that parses today can change
meaning. gopher-lua's own test suite passes with the patch applied.

### The one place it does not follow Lua 5.3

gopher-lua holds every number as a `float64`; Lua 5.3 has true 64-bit
integers. Integers are therefore exact only to 2^53.

Rather than return a plausible-looking wrong answer, an operand or result that
will not survive the round trip **raises**:

```
number 1.152921504606847e+18 is too large for an exact integer operation
result 9007199254740993 is too large for this implementation to represent exactly
```

These operators compute image addresses. A silently wrong address is a wrong
page, which nothing downstream could detect; a module that stops is at least
visible. Nothing in the catalogue comes near the limit — the largest
intermediate is a 32-bit shuffle whose product peaks around 7.1e15, just under
9.007e15.

Every expected value in `internal/scraper/lua53_test.go` was taken from an
independent Lua 5.4 implementation (`arnodel/golua`) rather than worked out by
hand.

### Applying it

```sh
# on your fork of github.com/yuin/gopher-lua, on top of the generic-for patch
git apply /path/to/atsume/patches/gopher-lua-lua53-operators.patch
git commit -am "add Lua 5.3 bitwise operators and floor division"
git tag v1.1.3-atsume && git push --tags
```

Then point `go.mod` at the new tag. Until that happens the tests in
`internal/scraper/lua53_test.go` skip, naming this patch as the reason.

### When it can be dropped

If atsume ever moves to a Lua 5.3+ runtime — `arnodel/golua` on its `lua5.4`
branch compiles the whole module corpus — both patches go with it, along with
the float-precision caveat above. That is a larger change: every binding in
`internal/scraper` is written against gopher-lua's API.
