# TypeScript sidecar

§3.2 asks for the TypeScript module graph and call sites from "the TS checker".
That means the TypeScript compiler API, which is JavaScript, which is why this
is a sidecar rather than Go.

## What it adds over the lexical analyzer

`internal/analyzers/typescript` reads the source and labels every edge with what
reading supports: imports resolved against the filesystem are `resolved`, bare
specifiers and heritage clauses are `declared`, and there is deliberately no
call graph — without a type checker an identifier in call position may be a
local, a shadowed binding or a method on an unrelated object.

This sidecar has a type checker, so it can answer those questions:

| Relationship | Lexical analyzer | This sidecar |
|---|---|---|
| module to module | `resolved` (filesystem) | `resolved` (compiler resolution, including `tsconfig` paths) |
| class implements interface | `declared` (syntax), skipped when ambiguous | `resolved` (the checker knows which symbol) |
| function calls function | not emitted | `resolved` (call sites from the checker) |
| type usage | not emitted | `resolved` |

The Go analyzer stays. The sidecar is optional: a repository without Node still
gets the lexical reading, and the evidence category says which one produced an
edge.

## Contract

Reads a repository root and writes JSON on stdout:

```console
$ node analyze.js /path/to/repo
{"nodes":[...],"edges":[...],"diagnostics":[...]}
```

Every edge carries `evidence`, matching the Go graph's vocabulary. Nodes and
edges use the same `ts:` FQN scheme as the lexical analyzer, so the two produce
the same identifiers for the same things and the graph does not double up.

`diagnostics` reports what the sidecar could not do — a missing `tsconfig.json`,
a file that failed to parse. An empty result with no diagnostics means the
repository genuinely has no TypeScript; an empty result *with* them means the
analysis did not happen, and the caller must not treat the two alike.
