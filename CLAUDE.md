# atsume

A manga downloader that runs FMD2's Lua website modules. The Go implementation
lives in `cmd/` and `internal/`; it is being ported to Rust in `crates/`, one
package at a time. Until the port is complete, the Go code is the reference for
behaviour: port what it does, including the upstream quirks its comments and
tests pin.

## Licensing

FMD2's modules are GPL-2.0-only, which is incompatible with atsume's
GPL-3.0-only. **Never commit a module file or anything derived from one**,
including test fixtures. Tests that need modules read `ATSUME_FMD2_DIR`; in
Rust they are marked `#[ignore]` and fail if run without it, and
`make rust-corpus` runs them. See `docs/MODULES.md`.

## Rust

### Before finishing any change

```sh
cargo fmt --all
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace
```

`make rust-check` runs all of these plus `cargo deny check`. Fix lint warnings
in the code; do not silence them with `#[allow]` unless the lint is wrong for
that line, and then say why in the attribute's `reason`.

### Clean code

- Keep functions short and doing one thing. When a loop body or match arm does
  real work, extract it into a named function.
- Return early instead of nesting. Use `let ... else`, `?` and guard clauses
  so the main path reads straight down.
- Choose names that make a comment unnecessary. A long name is better than a
  short one plus a comment.
- Report failures with `Result` and the crate's error type. Do not `unwrap`,
  `expect` or `panic!` outside tests.
- Prefer plain code over clever code: no macro, trait or generic unless it
  removes real repetition.
- Write a test for each behaviour you port, and port the Go test that covers
  it when one exists.

### Comments

- Explain why, not what. If a comment restates the code, delete it or rename
  something.
- A doc comment opens with one sentence saying what the item does. For a
  field or a variant, that opening may be a noun phrase naming what it holds
  ("The cover image's address."). Add more only for what a caller must know,
  such as errors, edge cases or an upstream contract.
- Write full, plain sentences that a reader new to the file understands
  without any other context. Every sentence has a subject and a verb, and a
  pronoun refers only to something the same comment has named.
- Keep comments short, but prefer three plain sentences to two crowded ones.
  Reasoning that needs a paragraph belongs in `docs/`, with the comment
  pointing there.
- Say directly what is true. Do not describe something by what it is not, and
  do not repeat a stock phrase from one comment to the next. When every item
  of a type needs the same remark, make the remark once on the type.
- No history: not what the code used to do, nor what was fixed. That belongs
  in commit messages.
- Do not state counts or facts that can go stale ("used by 113 modules")
  unless a test keeps them checked.
- Name each Rust item after what it holds or does, even when FMD2 uses a
  different name for it. Keep the FMD2 name easy to find: it stays in the
  string that binds the item to Lua, and the item's doc comment mentions it in
  backticks, as in "Modules read and change it through `HTTP.UserAgent`."
