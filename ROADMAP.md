# Roadmap

The phase plan from the design, with what is actually done marked honestly. An
unticked box is unimplemented, not "mostly working".

## Where things stand

**Implemented and tested**: workspace identity and the isolation contract,
per-workspace SQLite storage with migrations and recovery, the typed code graph
with evidence categories and impact analysis, the Go language analyzer and the
commit-history analyzer, retrieval with the provenance guard, the intent-first
execution journal with crash recovery and leases, the three-layer sandbox, the
process manager, per-task worktrees, the verification recipes with output
summarisers, the completion contract, the provider abstraction with four
provider kinds, hardware profiles and `le models bench`, the supervisor API and
dashboard, `le doctor`, container images and compose files, and the CI gates.

A task runs end to end today: `le task verify` creates a worktree, syncs your
uncommitted changes into it, runs build, vet and test inside a Landlock
sandbox, records every result as evidence tied to the exact content hash it
describes, and decides acceptance from that evidence alone.

**Not implemented**: the engine adapter that produces edits. The pipeline that
judges a change is complete; what fills the worktree with a *model's* change is
not. See Phase 3 below.

---

## Phase 0 — Repository and foundations ✅

- [x] Repository scaffold with governance files
- [x] CI gates: format, vet, lint, staticcheck, race tests, schema validation,
      short fuzz, CodeQL, govulncheck, osv-scanner, gitleaks, commit-lint
- [x] Scorecard workflow on `main`
- [x] CPU image builds, starts and passes `/readyz`
- [x] Documentation command blocks executed by CI
- [x] `le models bench`

**Exit condition met.** The image starts and passes `/readyz`; the docs test
executes every marked command block and fails the build on stale documentation.

## Phase 1 — Isolation and the single container ✅ (core)

- [x] Workspace identity, pinned and adoptable
- [x] `internal/store` scoping, enforced by the `storescope` analyzer
- [x] Landlock runner, verified against a real kernel
- [x] Single-container process model with health checks and backoff
- [x] Two-workspace isolation tests, and proof they fail when isolation breaks
- [x] `le doctor` reports which DR-3 layers are active
- [x] Per-task worktrees, so a task never edits the operator's checkout
- [ ] First model-authored bug fix completes inside the container *(needs the engine adapter)*

## Phase 2 — Crash safety ✅ (core)

- [x] Execution journal: intent before the side effect, outcome after
- [x] Recovery: reconcile, classify uncertain operations by inspection,
      reconstruct working state, mark stale evidence
- [x] Worktree leases with expiry
- [x] `le backup` and `le restore`, round-trip verified
- [x] Every task attempt journalled intent-first, with a checkpoint on finish
- [ ] Full Stage D interruption-class suite driving long-running model tasks

## Phase 3 — Task execution 🟡 (deterministic half done)

- [x] Engine adapter contract (`internal/engine`), with a verification-only
      implementation that exercises the whole pipeline without a model
- [x] Per-task worktrees and the sandbox spec for each
- [x] Verification levels (low, standard, high) and the completion contract
- [x] Evidence summarisers for compiler, vet, test, race and format output
- [x] Out-of-scope write detection against a declared scope
- [ ] **The OpenCode integration** — the adapter that turns a packet into edits
- [ ] Task decomposition into bounded child tasks
- [ ] The broker and human gates

The completion contract is the part that matters most and it is done: a task
is accepted only when every recipe its level requires has a **passing** result
produced against the **current** candidate, with no out-of-scope writes. A
skip, an error, or a pass against an older candidate satisfies nothing, and an
engine's claim that it finished is an input to that decision, never the
decision.

## Phase 4 — Language analyzers and the full graph 🟡 (Go done)

- [x] Go: modules, packages, imports, call graph, interface satisfaction,
      type usage, signatures, struct fields, tests, configuration keys, routes
- [x] Commit history as a first-class relation
- [x] Storage and graph benchmarks published
- [ ] TypeScript: program module graph, call sites, references
- [ ] Schema edges: `pg_query_go` over migrations, sqlc, proto
- [ ] Build, deployment and infrastructure edges: Dockerfile, Makefile,
      compose, Helm, Terraform

Every Go relationship from the design's coverage table is implemented and
tested against a real type-checked fixture. The per-language state is in
[the graph schema reference](docs/reference/graph-schema.md).

## Phase 5 — Evaluation ⬜

- [ ] Task set and harness
- [ ] The (a) unsupervised / (b) supervised / (c) frontier comparison
- [ ] Graph ablation: measure the graph's contribution rather than assume it
- [ ] Published results with hardware disclosure

Until this phase produces numbers, the README claims nothing about success
rates.

## Phase 6 — Breadth ⬜

- [ ] Provider implementations beyond the current four
- [ ] Profiles for other hardware classes
- [ ] Split compose variant exercised in CI
- [ ] Optional semantic retrieval, if Phase 5 shows it earns its cost
- [ ] Vision for UI review

## 1.0

- [ ] Everything above
- [ ] The acceptance matrix passing end to end
- [ ] Signed release with SBOM, provenance and a published benchmark snapshot

Versioning stays at `0.y.z` until then.

---

## Known limitations, restated

These are in the README too, and `le doctor` reports them at runtime:

- Process-level isolation between concurrent tasks needs the optional
  bubblewrap layer, which is usually unavailable inside a container.
- Landlock's TCP rules do not cover Multipath TCP.
- SQLite needs a real filesystem; overlay layers and network shares are
  refused.
- Downgrades across a schema version are not supported.
- Shipped hardware profiles are starting points, not measurements.
