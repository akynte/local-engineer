# Roadmap

The phase plan from the design, with what is actually done marked honestly. An
unticked box is unimplemented, not "mostly working".

## Where things stand

**Everything design v3 specifies is now built.** The six gaps an audit found in
September — the egress proxy, the TypeScript sidecar's absence from the image,
§4.2's missing tools, the bare-metal install script, §10.1's runtime-feedback
and generation rows, and `le tui` / the telemetry aggregate / the docs site /
the demo — are closed. What remains open is evidence, not features: the task set
is still too small to discriminate between arms (Phase 5 below).

**Implemented and tested**: workspace identity and the isolation contract,
per-workspace SQLite storage with migrations and recovery, the typed code graph
with evidence categories and impact analysis, the Go, SQL-schema, build,
deployment, Terraform, TypeScript and commit-history analyzers, retrieval with
the provenance guard, the intent-first
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

**Not implemented**: nothing from §1.2's layout is missing. `workflows/` is
empty on purpose — §1.2 lists it and v3 defines it nowhere, so it carries a
README saying so rather than an invented mechanism.

Every §4.2 tool except the Playwright browsers now ships, and the rule for what
belongs there is written down in
[the image manifest](docs/reference/image-manifest.md): **the engine has no
shell tool.** Its only route to an external program is `run_recipe`, so a
binary no recipe invokes is unreachable rather than optional. `golangci-lint`
and `semgrep` have built-in recipes; `gosec`, `gitleaks`, `osv-scanner`,
`squawk`, `buf` and `oasdiff` are reached through a `check:` step in
`.le/verify.yaml`, because which tables a migration may lock and which proto
changes are breaking are facts about a repository.

**Playwright is still absent**, and [verify item 5](docs/reference/verify-list.md)
tracks it: no image ships a UI for a browser to drive, and 300 MB of browsers
that nothing runs is not a capability. An `integration:` step is how you use one
if you install it yourself.

**Trivy is absent for a different reason**: it scans dependencies for
vulnerabilities, so a task would fail for a CVE published between two runs. That
belongs in CI, where it is, not in a task's completion contract. A repository
that disagrees can declare it as a `check:` step and install it.

The §8.3 needle test has been run against a local model and the result is
published in `docs/benchmarks/results/`. It found **no retrieval ceiling at any
size this hardware can serve**. A 35B MoE recalled a random code at every depth
of every size from 8,000 to 64,028 measured tokens — 50 probes, no misses. The
context window was doubled from 32,768 to 65,536 specifically to look for the
degradation point, and it is still not there. The model's trained context is
262,144, so none of this was measured past what it was built for.

So the packet cap here is set by what the hardware can serve, not by what the
model can retrieve from, and `max_packet_tokens` was deliberately left where it
was: the measurement says the current value is safe, not that a larger one is
warranted. Raising it costs prefill on every step in exchange for a packet
retrieval may have nothing to put in.

Getting there took four runs, three of which were wrong by 14%, 5.8% and 3.1%
in the axis the answer is read off, plus two separate false-ceiling bugs — a
refused request and a truncated answer were both being recorded as the model
failing to retrieve. None of the five was visible without looking at the numbers
the tool printed beside its own conclusion, which is the argument for printing
both.

The evaluation harness has been run against a local model — 3 tasks × 4 arms ×
5 passes, 60 runs, published in `docs/benchmarks/results/`. It settled nothing:
every arm's interval overlapped every other's and a third of the cells changed
verdict between passes. The run's own conclusion was that the fixtures were too
small — at 17 to 87 lines of Go, reading the whole repository fits in one packet,
so retrieval and the graph had nothing to contribute by construction. A
2,075-line fixture and seven tasks over it now exist to answer that; whether
they discriminate is being measured, not asserted.

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

## Phase 2 — Crash safety ✅

- [x] Execution journal: intent before the side effect, outcome after
- [x] Recovery: reconcile, classify uncertain operations by inspection,
      reconstruct working state, mark stale evidence
- [x] Worktree leases with expiry
- [x] `le backup` and `le restore`, round-trip verified
- [x] Every task attempt journalled intent-first, with a checkpoint on finish
- [x] Full Stage D interruption-class suite: all eight classes, each crossed
      with the three worktree conditions, asserting the recovery verdict comes
      from the worktree and never from how the process stopped. `kill -9` and
      `SIGTERM` kill a real child process rather than simulating one

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
the **current** candidate, **no check that ran found a problem**, and there are
no out-of-scope writes. A skip, an error, or a pass against an older candidate
satisfies nothing, and an engine's claim that it finished is an input to that
decision, never the decision.

The fourth clause was missing until the lint recipe was built and exercised
end to end: `Accept` consulted only the kinds the level *requires*, and the
conditional kinds — `lint` and `analyzer`, which run only where the repository
committed a configuration — cannot be on that list without failing every
repository that committed neither. So a failing semgrep run produced an
accepted task. Requiring evidence and forbidding a failure are two different
questions, and only the first depends on the level.

**A note on DR-5.** The design chose OpenCode as the execution engine. What
shipped is a native engine driving the provider boundary directly: the adapter
contract is what DR-5 is actually about, and a native implementation is
testable end to end without an external process, uses the provider abstraction
that already exists, and works with any OpenAI-compatible backend. An OpenCode
adapter remains a legitimate second implementation of `engine.Engine` — the
contract was designed for exactly that. This is now recorded properly rather
than as a footnote: [DR-7](docs/adr/0007-native-engine.md) supersedes DR-5,
with the argument and its costs written out.

## Phase 4 — Language analyzers and the full graph ✅

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
- [x] **TypeScript**: module graph, declarations, heritage and config reads —
      lexical, without a type checker, and every edge carries the evidence
      category that reading actually supports

An impact report now crosses analyzer boundaries: changing a table finds the Go
queries that read it, and changing a configuration key finds the code, the
compose service, the Dockerfile stage and the Terraform resource in one
traversal.

Two things are recorded as deviations rather than done quietly:

- **The schema parser is not `pg_query_go`.** That is cgo, and the CGO-free
  build is load-bearing for DR-1's static multi-arch binary. The DDL subset is
  narrow and *reports* what it could not parse. See
  [the graph schema reference](docs/reference/graph-schema.md).
- **TypeScript has two readings, and the graph records which one it got.**
  Real type checking means the TS compiler API, which means a Node sidecar.
  That sidecar is built (`sidecars/typescript/`), tested in CI, wired into
  `le index`, and shipped in the `cpu` and `cuda` images; its edges — including
  the call graph — are `resolved`. Where it is absent (the `-slim` image, or a
  host with no Node) the analyzer reads the source and records what reading
  supports: imports resolved against the filesystem are `resolved`, bare
  specifiers and heritage clauses are `declared`, `process.env` reads are
  `inferred`, and there is deliberately **no call graph** — without a type
  checker an identifier in call position may be a local, a shadowed binding or
  a method on an unrelated object, and an edge that is wrong half the time is
  worse than no edge. An ambiguous heritage name produces no edge for the same
  reason. This is the §3.2 evidence model doing its job: the read rows are
  weaker than the checked ones and say so, rather than being absent or
  overstated.

## Phase 5 — Evaluation 🟡 (run once; the task set could not discriminate)

- [x] Task set and harness, with hidden acceptance, protected paths, and
      ground truth recorded separately from the system's own verdict
- [x] The arm set: unsupervised, supervised, and the frontier calibration
- [x] Graph ablation and verification ablation, so each component's
      contribution is measurable rather than assumed
- [x] Statistics that refuse to overclaim: Wilson intervals on every rate, and
      a difference called significant only when the intervals do not overlap
- [x] Task-set validation in CI: acceptance must fail on the untouched fixture,
      and a reference solution must pass
- [x] **A run against a real model on disclosed hardware** — 60 runs, local
      35B MoE, hardware disclosed with the results
- [x] Published results — `docs/benchmarks/results/2026-09-14-tasks.md`

**Measured, and the measurement says the task set is not up to the job.** 60 runs
against a local 35B MoE on disclosed hardware. No comparison is statistically
detectable; 4 of 12 task/arm cells changed verdict between passes; two of the
three tasks are solved by every arm on every pass, including the bare baseline.

Running it for the first time found six defects the test suite could not see,
three of which corrupted the measurements themselves (conflated prefill/decode
timing, prompt-cache hits counted as prefill, token totals never summed) and two
of which would have produced publishable-looking but meaningless numbers: the
supervised arms retrieved from an unindexed workspace, and every tool was offered
to arms that could not run it. Fixed with regression tests in `dfaf21b`.

What is genuinely open after the run:

- [x] **Repetitions as standard.** `le eval run --repeat` defaults to 3. With
      the default at 1 the invocation that produced a publishable-looking table
      was the one that measured nothing, and the honest setting was the one an
      operator had to remember.
- [ ] A task set large enough to discriminate — 30+ tasks, with fixtures big
      enough that reading the whole repository is not a strategy. **10 today,
      7 of them on the 2,075-line fixture**, up from 8 and 5. Each new task is
      mutation-checked: the wrong-but-passing answer it names is applied and
      required to fail.
- [ ] The graph's contribution, still *measurable* rather than *measured*: on
      the published set it added zero points and roughly doubled the tokens
      spent on the only task that discriminated. A run over the large-fixture
      tasks at 4 arms × 5 repetitions is what would change that, and one
      supervised task on that fixture takes about 7 minutes on the reference
      hardware — so the answer costs GPU-hours, not a code change.

The distinction still matters because a repository containing an evaluation
harness looks like one with evaluation results — and now, one containing
evaluation results looks like one whose results mean something.

## Phase 6 — Breadth ⬜ **next**

- [x] `le models conformance`: checks a provider against what providers.yaml
      declares about it, because DR-4 makes those declarations something
      callers rely on and nothing verified them
- [x] **The §6.1 allowlisting egress proxy and its provisioning lanes.** One
      listener per lane, an allowlist of exact hosts or single leading
      wildcards with a required reason each, ports 80 and 443 only, every
      decision journalled. Off by default, refused alongside `offline`. A task
      is never granted its port — and there is no flag that would, because the
      only function that builds a spec containing it takes a `proxy.Lane`.
- [x] **Repository-declared runtime and generation checks** (`.le/verify.yaml`),
      closing §10.1's last two adopted rows. A generate check runs the
      generator, compares, and restores — so it reports whether committed
      output is current without mutating the worktree and invalidating every
      other result.
- [x] **`le tui`** (§4.1), read-only on purpose; **the optional cross-workspace
      telemetry aggregate** (§2.2), counters only, built on demand;
      **`scripts/install-bare-metal.sh`** (§13); **the docs site** (§1.1),
      built strictly in CI; **the asciinema demo** (§15), generated from real
      command output rather than written
- [ ] Provider implementations beyond the current four
- [x] Profiles for other hardware classes: 16 GB and 24 GB CUDA, Apple Silicon
      unified memory, and a CPU-only MoE profile — all unmeasured starting
      points that say so, as §9.2 requires
- [x] Split compose variant exercised in CI — brought up against a stub
      inference service and checked for the claim it makes, not just parsed
- [ ] Optional semantic retrieval, if Phase 5 shows it earns its cost
- [x] Vision at the provider boundary: images on a message, encoded for both
      wire formats, and refused rather than dropped by a provider that does not
      declare it. Using it for UI review needs a vision model configured, which
      no shipped profile assumes

## 1.0

- [ ] Everything above
- [ ] The acceptance matrix passing end to end
- [x] Signed release with SBOM, provenance and a published benchmark snapshot —
      Sigstore signing, SPDX SBOMs and SLSA provenance were already wired;
      `BENCHMARKS.md` is now assembled from the committed results and attached,
      with CI failing if it drifts from them

**The acceptance matrix has no specification here.** v3 defers it to "the v2.0
Section 18 matrix", and v2.0 is not in this repository — v3 is the only source
of truth available. The same is true of "Stage A", which v3 names in the Phase 6
exit condition and in the model-report issue form without ever defining it.

Two things follow, and both are recorded rather than papered over:

- What v3 *does* state per phase is a table of exit conditions (§14), and those
  are testable. Phase 6's is "a second provider passes Stage A; a CPU-only
  profile runs the tutorial end to end".
- `le models conformance` is what Stage A appears to be from how v3 uses it — a
  per-model capability report an operator can paste into an issue. It was built
  from that description, not from the v2.0 definition, and it is named for what
  it does rather than for a stage nobody here can read.

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
