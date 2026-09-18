# Local Engineer: assessment against the architecture review

Assessment date: 17 September 2026. Source revision: `9e07fe1`, including the current working tree. Requirements: `local-coding-system-review.md`.

## Verdict

**Partially meets the need. A substantial foundation exists, but the requested autonomous, cache-aware, phase-controlled coding system is not implemented end to end.** Keep the working storage, analyzers, verification, worktrees, model client, and evaluation harness. Completing this requires changes to orchestration and enforcement, not just configuration.

This is an implementation comparison against the supplied document. Its external model, runtime, and research claims were not independently fact-checked. No live inference benchmark, Docker deployment, or task on the user's production repositories was run. Shipped profiles and committed measurements are distinguished from currently active host configuration, which was not audited.

## Highest-priority gaps

### 1. Context handling violates the append-only requirement

Requirements: §§7, 23–25.

`internal/engine/native/native.go:165` implements `trimMessages`, which removes earlier assistant/tool exchanges during an edit attempt. The loop calls it before requests at line 270. Ordinary turns append messages and retain the initial system/user messages, but trimming changes the earlier transcript within the phase. Each attempt also starts a new seed, and the runner rebuilds retrieval at `internal/task/runner.go:485`.

There is no explicit persisted phase log with phase-boundary-only compaction. `CachePrefixHint: 2` is not proof of actual llama.cpp cache reuse. On the caching assumptions in the review, the current behavior can invalidate reuse on long edits. Measure this before estimating the latency impact.

Needed: frozen prefix, append-only phase transcript, explicit budget boundary transitions, durable structured state for compaction, and cache-hit/full-reprocessing measurements on real edit sequences.

### 2. Plan scope does not prevent writes

Requirements: §§8–9, 14.

`internal/task/task.go:75` explicitly defines scope as a post-edit diff check, with empty scope allowing the entire worktree. Native file tools (`internal/engine/native/exec.go:128`) call worktree read/write helpers without receiving a plan allowlist. `internal/worktree/worktree.go:455` confines paths to the worktree but does not deny in-repository `.env`, PEM/key, or credentials files. Scope/policy checks occur after execution and verification in `internal/task/runner.go:387`.

Consequently, rejection can prevent acceptance of an unwanted change but does not prevent the write or a protected file read. The edit request has no phase or allowed-path field (`internal/engine/engine.go:22`).

Needed: validate every call against its schema, current phase, canonical path, secret-read rules, generated-file rules, and plan allowlist before execution.

### 3. OpenCode integration is cooperative supervision

Requirements: §§4–5, 9, 14, 23.

`internal/opencode/agents.go:82` tells OpenCode to open a task and then use its own tools. `internal/opencode/config.go:31` registers an MCP server. `internal/mcp/supervise.go:31` exposes task start/verify/finish tools. These provide useful recording and verification, but do not launch OpenCode as an EDIT-only sandboxed process or force its native edits/shell calls through the supervisor firewall.

The native engine has a constrained tool surface and verification commands use the project's sandbox machinery. Those controls should not be mistaken for containment of the separate OpenCode process.

Needed: an enforced editor execution boundary, with the native editor retained as a fallback.

### 4. The required phase machine and rejection loop are absent

Requirements: §§8, 11.

The current task lifecycle has pending/running/paused/blocked/review/accepted/failed states (`internal/task/task.go:31`). Its execution is an attempt loop: retrieve, edit, verify, accept or retry (`internal/task/runner.go:331`). Planning is a separate decomposition operation, not a required phase on every task. There is no mandatory structure → symbol → confirmation localization sequence.

Fresh-context review exists, but is explicitly advisory; failure to run it is nonfatal (`internal/task/runner.go:1014`). Acceptance is set before review. This differs from the document's structured accept/reject review gate and repair/re-localization transition.

Needed: persisted phase transitions with entry/exit conditions and tool authorization. The document itself says “six phases” but names eight including INTAKE, IMPACT and FINALIZE; implement the named responsibilities rather than optimizing the count.

### 5. Retrieval and impact cover only part of the requested stack

Requirements: §§6, 10, 16.

There is real compiler-backed Go analysis and a TypeScript compiler sidecar, plus SQL, protobuf/Avro, deployment, Terraform, architecture and git-history analyzers (`internal/supervisor/supervisor.go:241`). Retrieval uses SQLite FTS anchors and graph expansion (`internal/retrieval/retriever.go:16`).

No SCIP loader, live LSP client, tree-sitter/PageRank repo-map implementation, or Rust analyzer was found in the implementation. The existing compiler-backed analyzers are useful alternatives for supported languages, but do not supply the required Go/Rust/TS/Vue coverage.

Impact reporting exists, but automatic impact obligations are not wired into the edit loop. The planner copies `pkt.Impact` after a retrieval request that does not set `ImpactOf`; the retriever only computes that report when `ImpactOf` is supplied (`internal/retrieval/retriever.go:181`). The runner likewise retrieves by task title without computing changed-symbol obligations before final review.

Needed: automatic pre-plan and diff-time impact computation, caller obligations, associated-test requirements, and structured coverage for the missing languages. Benchmark existing analyzers before replacing them solely to match library choices.

### 6. Failure recovery and budgets are incomplete

Requirements: §§8–9, 11.

The native loop detects repeated identical tool calls/results (`internal/engine/native/progress.go`). The runner bounds attempts and wall time and can ask for a fresh diagnosis after two failures (`internal/task/runner.go:1040`).

This is not the requested normalized verification-failure fingerprint database and NEW/SAME/REGRESSION/ENVIRONMENTAL/FLAKY classifier. There is no baseline-test comparison, automatic restore-to-verified checkpoint/re-localize ladder, or environmental-pause/flaky-rerun routing in the attempt loop. Failed task changes are retained and committed for inspection rather than restored to the start state.

Also, the runner passes task `MaxTokens` in `engine.Request.Budget`, but the native loop uses its configured per-response `MaxTokens` and does not enforce that request's cumulative token limit.

### 7. Model configuration and performance evidence only partially match

Requirements: §§2–3, 7, 15, 22–25.

The committed task report documents Qwen3.6-35B-A3B UD-Q4_K_XL on the reference hardware: 416 tok/s prefill and 35 tok/s decode (`docs/benchmarks/results/2026-09-14-tasks.md:112`). This is existing evidence, not a new measurement from this assessment.

The shipped reference profile still uses two slots, 32K context per slot, different sampling/thread settings, and no explicit checkpoint/MTP configuration (`profiles/reference-8gb-cuda-64gb-ram.yaml`). Extra arguments can configure runtime options, so these are not all missing capabilities. Role routing exists, but an automatic stop/load swap between daily and deep models and the requested per-phase reasoning budgets were not found.

The evaluation is three tasks × four arms × five passes. Its own report says confidence intervals overlap and the graph's benefit is not established. This does not satisfy the requested 30–50 historical tasks, held-out split, component ladder, inference-depth/cache/MTP matrix, or removal rule.

## Other requirements

| Area | Assessment |
|---|---|
| Go supervisor; SQLite; no agent framework or model crew | Meets the architectural direction. |
| Workspace-scoped storage, worktrees, journal and recovery | Substantial implementation with tests. Identity is path/remote/name based, not the requested clone-stable root-commit/normalized-remote scheme (`internal/workspace/identity.go:86`). |
| Deterministic verification tied to current candidate | Strong implementation; stale evidence, missing required kinds and scope violations can block acceptance. |
| Go/Rust/frontend verification | Built-ins are Go-focused (`internal/recipe/builtin.go:65`). Declared commands extend checks, but automatic Rust/TS/Vue presets and impact-selected test scope are not implemented as requested. |
| OS isolation | Landlock and optional bubblewrap exist. Actual deployment guarantees depend on available layers; do not equate command sandboxing with OpenCode confinement. |
| Project memory | Versioned YAML notes with provenance exist under `.le/memory`. The requested project/architecture/conventions/constraints/glossary markdown structure and symbol-based stale-note checks are absent. Different filenames alone are not a functional blocker. |
| Trace and crash recovery | Ledger/artifacts/telemetry exist. The native attempt is journalled as an engine step; a durable replayable per-call phase transcript is not implemented. |
| Embeddings/reranker | Their absence is consistent with the measure-first requirement; not a blocker. |
| Packaging | Docker distribution and substantial documentation exist. Packaging readiness does not establish workflow completeness. |

## Recommended completion order

1. Extend the existing inference measurements with the exact cache/depth/MTP matrix. Establish measured daily/deep profiles.
2. Implement the frozen-prefix phase log and enforce phase/token boundaries.
3. Enforce plan-scoped tools and secret-read policy before side effects, including the editor boundary.
4. Connect mandatory localization, planning, impact obligations, verification, review rejection and failure recovery into one persisted workflow.
5. Complete Rust/frontend structural and verification support for the actual repositories.
6. Expand evaluation with historical tasks and held-out component comparisons before adding optional retrieval models.

## Validation and working-tree notes

`GOCACHE=/tmp/le-review-go-cache go test ./...` passed after rerunning outside the restricted tool sandbox. The first run failed where tests tried to open localhost sockets; those were sandbox-induced failures. This is the Go test suite, not the complete `make check`, sidecar test suite, or live system acceptance benchmark.

No implementation files were changed. The pre-existing deletions of `LOCAL-ENGINEER-DESIGN-v3.md` and `ROADMAP.md`, and the untracked requirements document, were left untouched. This assessment file is the only added deliverable.
