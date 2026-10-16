# 1. Lua 5.4 through mlua for the Rust port

**Status:** Accepted

## Context

The Go version runs FMD2's modules on golua, a pure-Go Lua 5.5. FMD2 targets
Lua 5.4, and nine of its modules assign to a `for` loop's variable, which 5.5
rejects. The modules are untrusted code, so the runtime must be sandboxed and
bounded.

## Decision

Use [mlua](https://github.com/mlua-rs/mlua) with Lua 5.4 compiled in
(`lua54`, `vendored`). Run each scrape on its own thread, synchronously: Lua
cannot await, and concurrency is capped by the worker count and per-site rate
limits.

## Consequences

- 622 of 625 upstream modules load, against 611 under golua.
- Building needs a C compiler, since Lua is compiled from source.
- C Lua does not verify bytecode, so `load` is restricted to source text.
- One global count hook enforces the CPU budget and cancellation, including
  inside coroutines and against `pcall` catching the termination.

## Alternatives

- **piccolo** (pure-Rust Lua): no C, but incomplete standard library and not
  production-ready.
- **Luau through mlua**: built-in sandboxing, but a different dialect without
  the bitwise operators (`&`, `|`, `<<`) the modules use.
- **mlua with async**: frees threads during HTTP waits, at the cost of async
  bindings and a single-threaded executor; not worth it at a few workers.
