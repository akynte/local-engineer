# DR-8: tree-sitter for language-agnostic structure, ending the CGO-free build

- Status: Accepted
- Date: 2026-09-18

## 1. Problem

The architecture review's §4 diagram names a tree-sitter layer: symbols,
signatures and the ranked repository map, for every language and for the files
no indexer covers. The repository had no tree-sitter, and two of the jobs the
review assigns to it were simply not done.

The first is §11.1's `primary_symbol`. A failure fingerprint hashed the tool,
the exit class, the normalised diagnostic and the primary file, but not the
declaration the error sits inside.

It is worth being exact about what that costs, because the obvious claim is
wrong: §11.1 keeps line numbers in the hash deliberately, so adding the symbol
does not make an error that moved down the file hash the same, and nothing here
claims it does. What it buys is discrimination and a usable query. Two identical
messages in two functions of one file were one failure and are now two. And
§11.2 sends a repeated failure back to localization "with the failure as the
query" — `Checker.Check` is a query the index can answer, where `order.go:412`
is a line that has already moved by the time anyone asks.

The second is worse because it is silent. Signature obligations — the mechanism
that stops an attempt declaring itself done while a caller it broke goes
unaccounted for — read the changed files, and skipped every file that was not
`.go`. There was no error and no note. An attempt could change a public Rust
function or a Vue component's props, the obligations check would find nothing to
account for because it never looked, and the task would proceed to review with a
clean report. On a stack that is mostly Rust services and Nuxt front ends, the
check was answering for a minority of the code and saying nothing about the
rest.

## 2. Alternatives

- **A cgo-free tree-sitter.** `wasilibs/go-tree-sitter` does not exist;
  the official `tree-sitter/go-tree-sitter` and `smacker/go-tree-sitter` both
  bind the C library. There is no maintained WASM port.
- **Answer both questions from SCIP and the LSP client instead.** SCIP stores a
  signature and a line range per symbol, and the LSP client added in the same
  increment answers for dirty files. This needs no new dependency and keeps the
  static binary.
- **Take tree-sitter and accept cgo**, ending the CGO-free build that
  [DR-1](0001-single-container-sqlite.md) and the
  [graph schema note](../reference/graph-schema.md) both rest on.
- **Do nothing**, and record the two gaps as known limits.

## 3. Evidence

- No cgo-free binding exists: `go list -m` resolves neither a wasilibs module
  nor any other WASM port; the two published bindings are cgo.
- The SCIP-and-LSP alternative is real but partial. SCIP answers only for code
  an indexer has processed, and the file that just failed to build is precisely
  the file the indexer could not process. The LSP client covers dirty files,
  but only where a language server is configured and running, which is operator
  setup this project cannot assume — and `le` must work on a machine with no
  language server installed.
- tree-sitter is error-tolerant by construction. It yields the declarations it
  recognised from source that does not compile, which is the state every file
  is in at the moment these two questions are asked.
- The cost is concrete and was measured rather than estimated: the build needs a
  C toolchain, the binary is dynamically linked against glibc, and the image can
  no longer produce amd64 and arm64 from one runner without a cross toolchain or
  emulation.

## 4. Chosen

Take `tree-sitter/go-tree-sitter` v0.25.0 with the Go, Rust and TypeScript/TSX
grammars, and end the CGO-free build for `le`. `storescope` and the other tools
stay static, because they do not need it.

The package answers two questions and no others: which declaration encloses a
line, and which signatures changed between two versions of a file. Resolution —
who calls what — stays with SCIP and the language servers, which know about
imports and types; a one-file parser does not.

Go keeps `go/parser` for signature comparison. It is exact, it handles the
receiver forms, and it is already covered by tests. tree-sitter is what the
other languages get, not a replacement for what worked.

## 5. Why

A safety check that silently answers for one language out of four is worse than
one that is absent, because it produces a clean report rather than a gap. That
is the argument that decides this: the obligations mechanism is load-bearing for
the claim that an attempt cannot declare itself done while it has broken a
caller, and on this codebase it was not making that claim for most of the code.

The alternative that keeps the static binary cannot close it. SCIP and LSP are
both better than tree-sitter where they apply — real compilers, real type
resolution — and neither applies to a file that does not compile on a machine
with no language server, which is the case that matters.

## 6. Disadvantages

- **No static binary.** `le` is now dynamically linked against glibc. A build
  on Debian bookworm does not run on Alpine; a musl target needs its own build.
- **The build needs a C toolchain.** Contributors need gcc or clang. `go install`
  from a machine without one fails, and the error names a compiler rather than
  this decision.
- **Cross-compilation is no longer free.** The image built amd64 and arm64 from
  one runner with `GOARCH`; that now needs a cross toolchain or emulation, and
  the image build is slower for it.
- **A C dependency in the supply chain.** Grammar modules ship generated C. They
  are pinned, and `govulncheck` does not see into them, which is a real
  reduction in what the existing tooling can tell us.
- **Grammar drift.** A grammar release can rename a node kind, and the symptom
  is not a compile error but a language quietly reporting no declarations. The
  tests parse real source in each language and assert named symbols, so drift
  fails a test rather than a task.
- **Vue is still not covered.** The published grammar has no Go module, so
  `.vue` files have no signature checking. `SignaturesSupported` reports false
  for them and the runner logs which changed files were not examined — the limit
  is stated rather than hidden, which is what the old behaviour failed to do.

## 7. Replacement path

If a maintained cgo-free binding appears, `internal/treesitter` is the only
package that imports the grammars and its exported surface is three functions;
swapping the backend is a change behind them, and restoring `CGO_ENABLED=0`
in the Makefile and `deploy/Dockerfile` is the rest.

If the decision is reversed instead, the two callers —
`workflow.enclosingSymbol` and `workflow.ChangedSignatures` — already degrade
to the old behaviour when `Supports` is false. Deleting the package leaves
fingerprints without `primary_symbol` and signature obligations Go-only, which
is the state this record was written to end; it should not be reversed without
something else answering those two questions.
