# Roadmap

The phase plan from the design, with what is actually done marked honestly. An
unticked box is unimplemented, not "mostly working".

## Where things stand

**Implemented and tested**: workspace identity and the isolation contract,
per-workspace SQLite storage with migrations and recovery, the typed code graph
with evidence categories and impact analysis, the filesystem/containment
indexer, retrieval with the provenance guard, the intent-first execution
journal with crash recovery and leases, the three-layer sandbox, the process
manager, the provider abstraction with four provider kinds, hardware profiles
and `le models bench`, the supervisor API and dashboard, `le doctor`, container
images and compose files, and the CI gates.

**Not implemented**: the task-execution loop. The system can index, query,
retrieve, journal and recover — it cannot yet run a task end to end. That is
Phase 3.

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
- [ ] First bug fix completes inside the container *(needs Phase 3)*

## Phase 2 — Crash safety ✅ (core)

- [x] Execution journal: intent before the side effect, outcome after
- [x] Recovery: reconcile, classify uncertain operations by inspection,
      reconstruct working state, mark stale evidence
- [x] Worktree leases with expiry
- [x] `le backup` and `le restore`, round-trip verified
- [ ] Full Stage D interruption-class suite driving real tasks *(needs Phase 3)*

## Phase 3 — Task execution ⬜ **next**

- [ ] Engine adapter contract and the OpenCode integration
- [ ] Per-task worktrees and the sandbox spec for each
- [ ] Task decomposition with executable acceptance criteria
- [ ] Verification levels and the completion contract
- [ ] Evidence summarisers for compiler, test and analyzer output
- [ ] The broker and human gates

## Phase 4 — Language analyzers and the full graph ⬜

- [ ] Go: packages, call graph, interface satisfaction, type usage, tests
- [ ] TypeScript: program module graph, call sites, references
- [ ] Configuration, schema, route, build, deployment and infrastructure edges
- [ ] Commit history as a first-class relation
- [x] Storage and graph benchmarks published

The graph schema already declares every relationship from the design's coverage
table, and the reference documentation marks which are implemented. Today that
is the filesystem and containment layer only.

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
