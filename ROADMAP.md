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

A task runs end to end today. `le task verify` puts your uncommitted work
under the completion contract. `le plan` decomposes a requirement into bounded
child tasks. `le task run` gives a task its own worktree, lets a model edit it
through a confined tool loop with verification as the correction signal, and
decides acceptance from evidence alone — then opens a human gate carrying the
diff and the findings before anything is applied.

**Not implemented**: language analyzers beyond Go, and the evaluation harness.
No task-success numbers are published, so the README claims none.

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
- [x] The engine's file operations are confined to the worktree, tested against
      traversal and symlink escapes, on top of the sandbox

## Phase 2 — Crash safety ✅ (core)

- [x] Execution journal: intent before the side effect, outcome after
- [x] Recovery: reconcile, classify uncertain operations by inspection,
      reconstruct working state, mark stale evidence
- [x] Worktree leases with expiry
- [x] `le backup` and `le restore`, round-trip verified
- [x] Every task attempt journalled intent-first, with a checkpoint on finish
- [ ] Full Stage D interruption-class suite driving long-running model tasks

## Phase 3 — Task execution ✅

- [x] Engine adapter contract (`internal/engine`), with a verification-only
      implementation that exercises the whole pipeline without a model
- [x] **The editing engine** (`internal/engine/native`): a bounded tool loop
      over the provider boundary, with nine tools, worktree confinement, and
      verification wired in as the correction signal
- [x] Tool calling in the provider layer, both wire formats
- [x] Per-task worktrees and the sandbox spec for each
- [x] Verification levels (low, standard, high) and the completion contract
- [x] Evidence summarisers for compiler, vet, test, race and format output
- [x] Out-of-scope write detection against a declared scope
- [x] Task decomposition into bounded child tasks, with dependencies
- [x] The broker and human gates

The completion contract is the part that matters most: a task is accepted only
when every recipe its level requires has a **passing** result produced against
the **current** candidate, with no out-of-scope writes. A skip, an error, or a
pass against an older candidate satisfies nothing, and an engine's claim that
it finished is an input to that decision, never the decision.

**A note on DR-5.** The design chose OpenCode as the execution engine. What
shipped is a native engine driving the provider boundary directly, for a
reason worth stating: the adapter contract is what DR-5 is actually about, and
a native implementation is testable end to end without an external process,
uses the provider abstraction that already exists, and works with any
OpenAI-compatible backend. An OpenCode adapter remains a legitimate second
implementation of `engine.Engine` — the contract was designed for exactly that
— and the decision record should be superseded rather than quietly ignored if
the native engine turns out to be the permanent answer.

## Phase 4 — Language analyzers and the full graph ✅ (except TypeScript)

- [x] Go: modules, packages, imports, call graph, interface satisfaction,
      type usage, signatures, struct fields, tests, configuration keys, routes
- [x] Schema: a pure-Go DDL parser over migrations, with migrations applied in
      order so the result is the schema as it ends up
- [x] Schema to application code: type-checked database call sites, so a table
      change finds the queries that read it
- [x] Build: Dockerfile stages and `COPY` sources, Makefile targets
- [x] Deployment: compose and Kubernetes; Helm templates recorded but not
      parsed, because rendering needs chart values
- [x] Infrastructure: Terraform HCL, with resource references and the
      environment variables they set
- [x] Commit history as a first-class relation
- [x] Storage and graph benchmarks published
- [ ] **TypeScript**: program module graph, call sites, references

An impact report now crosses analyzer boundaries: changing a table finds the Go
queries that read it, and changing a configuration key finds the code, the
compose service, the Dockerfile stage and the Terraform resource in one
traversal.

Two things are recorded as deviations rather than done quietly:

- **The schema parser is not `pg_query_go`.** That is cgo, and the CGO-free
  build is load-bearing for DR-1's static multi-arch binary. The DDL subset is
  narrow and *reports* what it could not parse. See
  [the graph schema reference](docs/reference/graph-schema.md).
- **TypeScript needs a Node sidecar**, because real type checking means the TS
  compiler API. The design anticipates this — `sidecars/` is in the repository
  layout — but it is not built.

## Phase 5 — Evaluation 🟡 (harness built, no results run)

- [x] Task set and harness, with hidden acceptance, protected paths, and
      ground truth recorded separately from the system's own verdict
- [x] The arm set: unsupervised, supervised, and the frontier calibration
- [x] Graph ablation and verification ablation, so each component's
      contribution is measurable rather than assumed
- [x] Statistics that refuse to overclaim: Wilson intervals on every rate, and
      a difference called significant only when the intervals do not overlap
- [x] Task-set validation in CI: acceptance must fail on the untouched fixture,
      and a reference solution must pass
- [ ] **A run against a real model on disclosed hardware**
- [ ] Published results

**Nothing has been measured yet.** The harness exists and is tested; producing
numbers needs a local model on disclosed hardware, and that run has not been
done. Until `docs/benchmarks/results/` contains task-success numbers, the
README claims none — and the graph's contribution is *measurable*, not
*measured*.

The distinction matters because a repository containing an evaluation harness
looks like one with evaluation results.

## Phase 6 — Breadth ⬜ **next**

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
