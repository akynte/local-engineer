# Local Engineer: Design v3.0 (Open-Source, Docker-First, Project-Isolated)

Version 3.0, 14 September 2026. Extends and amends the Final Design v2.0 to satisfy the ten core constraints in the project brief of the same date. Sections that v2.0 already satisfies are referenced, not repeated. Where the two conflict, this document wins.

Labels as in v2.0: [VERIFIED], [REPORTED], [BENCHMARK], [INFERENCE], [RECOMMENDATION], [VERIFY].

---

## 0. What changes, and why

The brief adds six requirements that v2.0 did not fully meet:

| Requirement | v2.0 state | v3.0 change |
|---|---|---|
| Public, professionally maintained open-source repository | personal tool | Repository standards adopted (Section 1); a public name, licence, governance and release process |
| Strict isolation between projects | one `project.db` per repo, implicit | An explicit **workspace identity** and isolation contract across every subsystem (Section 2) |
| Docker-first install and use; minimal host dependencies | host binaries, host bwrap, host llama-server | Single-container default distribution with a documented process model (Section 4) |
| Single container including storage | already SQLite | Kept; formalized as a decision record with trade-offs (Section 5) |
| Fully model-agnostic | OpenCode providers plus llama-server | An explicit provider abstraction with hardware profiles as configuration, not code (Section 9) |
| Decision records in a fixed seven-point format | ad hoc | Every major decision below uses the format (Section 12) |

Everything else in v2.0 (Go supervisor, OpenCode as sandboxed engine, adapter contract, broker, evidence-by-task-type, intent/observation/advice memory, typed graph with evidence categories, verification levels, completion contract, evaluation stages, acceptance matrix) stands.

One consequence must be stated plainly: **the v2.0 sandbox design assumed host bubblewrap with user namespaces.** Inside a container that is not the default. Section 6 replaces it with a layered design (container boundary plus Landlock plus optional bwrap) and documents the trade-offs.

---

## 1. Open-source project standards

### 1.1 Research summary (September 2026) [VERIFIED unless noted]

What distinguishes serious open-source projects today, beyond a README:

- **Supply-chain posture is visible and automated.** OpenSSF Scorecard (v5 introduced structured, per-heuristic results) and the OpenSSF Best Practices badge are the common public signals. Scorecard checks include branch protection, code review, pinned dependencies, signed releases, token permissions, SAST, fuzzing, SBOM and a security policy. Release provenance follows SLSA (Build L3 via `slsa-github-generator` is the usual target for GitHub-hosted Go projects) and artifacts are signed with Sigstore. SBOMs ship in SPDX or CycloneDX. Egress-filtered CI runners (Harden-Runner) are increasingly used in hardened workflow sets.
- **Reproducible, verifiable releases.** GoReleaser with `-trimpath`, pinned toolchain, checksums, provenance attestations, multi-arch container images with SBOM attached, semantic versioning, a generated changelog from Conventional Commits (release-please or GoReleaser changelog groups).
- **Documentation follows a structure.** The Diátaxis split (tutorials, how-to guides, reference, explanation) is the widely adopted pattern for developer documentation; architecture is documented with C4-style diagrams and ADRs (MADR template); a `docs/` site built from Markdown is preferred over a wiki.
- **Onboarding is containerized.** `docker compose up` or a single `docker run` for users; a Dev Container definition for contributors; a `Makefile` or task runner with `make check` reproducing CI locally.
- **Community readiness is explicit.** `CONTRIBUTING.md`, `CODE_OF_CONDUCT.md` (Contributor Covenant), `SECURITY.md` with a private reporting path (GitHub private vulnerability reporting), `CODEOWNERS`, `GOVERNANCE.md` or a maintainers file, issue forms (YAML) and a PR template, a `SUPPORT.md`, a public roadmap, labelled good-first-issues, a DCO sign-off or CLA decision.
- **Dependency hygiene.** Dependabot or Renovate with grouped updates, pinned GitHub Actions by SHA, `govulncheck` and `osv-scanner` in CI, `go mod verify`, a lockfile for the Node sidecars.
- **Quality gates in CI.** `golangci-lint` with a committed config, `gofmt`/`goimports`, `staticcheck`, race tests, coverage reported (as a diagnostic, not a gate), CodeQL, fuzz targets run briefly on PRs and longer nightly, integration tests in containers, benchmark tracking with `benchstat` against the base branch.
- **Measurability.** A published benchmark methodology, a `BENCHMARKS.md` with hardware disclosure, reproducible scripts, and result history in the repository.

### 1.2 Applied to this repository

Repository name: `local-engineer` (working title; a memorable final name is a product decision). Licence: **Apache-2.0** [RECOMMENDATION] (explicit patent grant, compatible with the Apache-2.0 models and MIT dependencies; note that OpenCode is MIT and llama.cpp is MIT, so redistribution in the image is compatible).

Top-level layout:

```
local-engineer/
  README.md  LICENSE  NOTICE  CHANGELOG.md  CONTRIBUTING.md  CODE_OF_CONDUCT.md  SECURITY.md  SUPPORT.md
  GOVERNANCE.md  MAINTAINERS.md  ROADMAP.md  CITATION.cff
  .github/  CODEOWNERS  ISSUE_TEMPLATE/(bug.yml, feature.yml, model-report.yml)  PULL_REQUEST_TEMPLATE.md
            workflows/(ci.yml, codeql.yml, scorecard.yml, release.yml, image.yml, nightly-fuzz.yml, bench.yml)
            dependabot.yml
  .devcontainer/  devcontainer.json  Dockerfile
  .goreleaser.yaml  .golangci.yml  .editorconfig  .pre-commit-config.yaml  Makefile  Taskfile.yml (one of the two)
  cmd/  internal/  sidecars/  prompts/  schemas/  workflows/  policies/  semgrep/  recipes/
  deploy/  Dockerfile  docker-compose.yml  docker-compose.split.yml  entrypoint/
  docs/  index.md  tutorials/  how-to/  reference/  explanation/  adr/  architecture/(c4-context.md, c4-container.md, c4-component.md)
        benchmarks/(METHODOLOGY.md, results/)  troubleshooting.md
  evals/  examples/(go-microservice/, nuxt-app/, monorepo/)  scripts/
```

CI gates on every PR: format, lint, `go vet`, `staticcheck`, unit tests with `-race`, sidecar tests, schema validation (all JSON Schemas compile; all YAML policies validate), integration tests in a container, short fuzz, CodeQL, `govulncheck`, `osv-scanner`, Scorecard (weekly on main), image build for amd64 and arm64 (CPU image) plus amd64 CUDA image, SBOM generation, signed release on tags with SLSA provenance.

Versioning: SemVer; `0.y.z` until the Section 18 acceptance matrix in v2.0 passes end to end, then `1.0.0`. Conventional Commits enforced by a commit-lint job; changelog generated at release time. Every release attaches: binaries, images (by digest), SBOMs, provenance, `BENCHMARKS.md` snapshot for the reference configuration.

Documentation set (Diátaxis):
- Tutorials: "First task in 15 minutes", "Index a Go microservice", "Run the reference evaluation".
- How-to: install, configure models, choose a hardware profile, persistent storage, start/stop/update, back up and restore, add a provider, add a language analyzer, write a recipe, write a policy rule, run fully offline, troubleshooting.
- Reference: CLI, configuration schema, tool schemas, graph schema, recipe format, telemetry schema, HTTP API of the supervisor.
- Explanation: architecture (C4 diagrams), why small models can work here, isolation model, trust boundaries, evaluation methodology, known limitations.

---

## 2. Strict project isolation

### 2.1 Workspace identity

A **workspace** is the unit of isolation. Its identity is:

```
workspace_id = base32(sha256(canonical_root_path || git_remote_url_if_any || user_supplied_name))[:26]
```

The canonical root is the resolved absolute path of the repository root (or of a declared multi-repo system root). A `.le/workspace.yaml` file inside the root pins the id, name, repositories and default branches; moving the directory without that file creates a new workspace on purpose, and `le workspace adopt` re-binds an existing id after a move. Multi-repository systems are one workspace with several repositories, each with its own repository id; a repository can belong to more than one workspace only through explicit declaration.

### 2.2 Isolation contract

Every subsystem stores state under `$LE_DATA/workspaces/<workspace_id>/` and nowhere else, and every query is scoped by `workspace_id` at the storage layer, not by convention:

| Subsystem | Storage | Scope enforcement |
|---|---|---|
| Source index, symbol index, graph, chunks, FTS, embeddings | `workspaces/<id>/index.db` (one SQLite file) | Separate database file per workspace; no cross-database queries exist in the code |
| Task ledger, execution journal, checkpoints, evidence references | `workspaces/<id>/ledger.db` | Separate file; supervisor opens one workspace per process instance |
| Artifacts (test output, diffs, screenshots) | `workspaces/<id>/artifacts/<sha256>` | Content-addressed under the workspace directory |
| Caches (package loads, call graphs, TS programs, doc notes) | `workspaces/<id>/cache/` | Keyed by workspace and content manifest; never shared |
| Memory files (intent, advice) | inside the repository under `.le/` | Travels with the repository; read only for the active workspace |
| OpenCode sessions and its own storage | `workspaces/<id>/opencode/` via `XDG_DATA_HOME`, `XDG_CONFIG_HOME`, `XDG_CACHE_HOME` set per task process | OpenCode never sees another workspace's directories |
| Model prompt cache and saved slots | llama-server slot files under `workspaces/<id>/slots/`; slots are cleared on workspace switch | Prompt-cache reuse requires an identical token prefix, so cross-workspace content leakage is not possible, but the switch still clears slots so that timing and cache statistics cannot leak either |
| Telemetry | `workspaces/<id>/telemetry.db` plus an optional aggregate with workspace ids only | Aggregate holds counters, never content |
| Temporary files | `workspaces/<id>/tmp/`, wiped on task end | Sandboxed processes see only this tmp |

There is no global semantic memory. "Cross-project lessons" are an explicit opt-in feature (`le lessons export/import`) that copies text you have read, never an automatic channel.

### 2.3 Enforcement and tests

- The storage layer exposes `OpenWorkspace(id) -> Store`; there is no API that takes a path or a table without a workspace handle. A linter rule (custom `go/analysis` analyzer) forbids `sql.Open` or file writes outside `internal/store` and `internal/artifacts`.
- Every retrieval result carries `workspace_id`, `repository_id`, `worktree_id`, path, symbol, content hash and index version (v2.0 Section 9.3). The context builder rejects slices whose `workspace_id` differs from the active task.
- Isolation tests in CI: index two synthetic repositories with overlapping symbol names, run retrieval and impact analysis in each, and assert zero cross-workspace rows, zero cache hits across workspaces, and zero cross-workspace slices in any packet. A "workspace switch" test asserts slot files are removed and the OpenCode data directory differs.

### 2.4 Comprehensive awareness of the active codebase

Within a workspace the system indexes files, directories, packages, modules, services, APIs, types, functions, classes, interfaces, dependencies, configuration, infrastructure, tests, build system and schemas as node kinds (v2.0 Section 10), plus the relationships in Section 3 below. "Awareness" is served by queries, not by prompt stuffing: the supervisor answers structural questions from `index.db` and hands the model only the slices the current step needs.

---

## 3. Code relationship graph and change-impact analysis

### 3.1 State of the art (September 2026) [VERIFIED papers; adaptation is ours]

- **Typed repository graphs improve localization.** LocAgent (ACL 2025) gives an agent graph-search tools over a heterogeneous code graph and reports strong file, module and function localization on SWE-bench Lite, including with fine-tuned 7B and 32B open models. RepoGraph (ICLR 2025) plugs an ego-network retrieval over a repository graph into SWE-agent and AutoCodeRover. CodexGraph indexes into Neo4j and lets the model write Cypher, which makes retrieval quality depend on the model's query skill, a poor fit for small models.
- **Lexical anchoring plus graph exploration** (LARGER, May 2026) combines BM25 anchors with graph traversal; **RepoMem** (October 2025) adds commit-history tools; "Code Isn't Memory" (June 2026) reports a leak-audited causal ablation of a structural index inside a coding agent. **Agentless** shows a deterministic localize-repair-validate pipeline is competitive without an agent loop.
- Takeaways adopted: (1) the graph is a tool the deterministic layer queries, not a database the model queries in a query language; (2) lexical anchors first, then graph expansion; (3) directory and file hierarchy are part of the graph; (4) commit history is a first-class relation; (5) measure the graph's contribution with an ablation, not by assumption.

### 3.2 Relationship coverage

All in the `edges` table of v2.0 Section 10.1, with evidence categories `resolved`, `declared`, `inferred`, `observed`, `unknown`:

| Relationship | Source of truth | Evidence |
|---|---|---|
| file to file, directory containment | filesystem, git tree | resolved |
| module to module, package to package | `go list`, `go mod graph`, TS program module graph, `package.json` workspaces | resolved |
| function to function (caller to callee) | VTA/CHA call graph; TS checker call sites | resolved (with VTA assumptions recorded) |
| interface to implementation | `types.Implements`; TS structural checks | resolved |
| type to usage | `types.Info` uses; TS references | resolved |
| import to dependency | compilers | resolved |
| API to consumer | route inventory plus client call sites with literal prefixes | inferred; declared via catalog |
| route to handler, handler to service, service to repository | router-registration call sites plus call graph; layer annotations from `ARCHITECTURE.md` | resolved plus declared |
| schema to application code | `pg_query_go` on migrations and embedded SQL; sqlc generated code names; proto and Avro parsers | resolved for parsed SQL, inferred for dynamic SQL |
| configuration to consumer | `os.Getenv`, `viper`, `envconfig` tags; compose and Kubernetes env mappings | resolved plus declared |
| test to implementation | call graph from `_test.go` and vitest files | resolved |
| build target to dependency | `go list -deps`, Docker `COPY`/`RUN` parsing, `Makefile` targets | resolved plus inferred |
| deployment component to service | compose, Swarm stack, Helm and Kubernetes manifests | declared |
| infrastructure resource to application component | Terraform (HCL parser) and manifests linked by names and env references | declared plus inferred |
| commit to file and symbol | `git log -L`, blame | observed |

### 3.3 Change-impact analysis

`impact_of(changed_nodes, change_kind)` (v2.0 Section 10.5) is exposed in three places: to the planner before edits, to the plan reviewer, and to the human gate. It reports consumers with evidence categories, deterministic compatibility verdicts, and required migration steps; consumers with `inferred` or `unknown` evidence are treated as present. A missing edge means "not discovered".

### 3.4 Synchronization

Index keys include the content manifest, lockfile hashes, toolchain, build mode and indexer version (v2.0 Section 10.4). A file watcher (`fsnotify`) inside the container marks packages dirty; re-analysis of dirty packages and their transitive dependents runs before any task step that needs the graph, and after every verified edit. `le doctor` reports index age and drift. Runtime observation (traces from integration tests) may add `observed` edges but never removes `resolved` ones.

### 3.5 Storage decision for the graph (see Section 12, DR-2)

Typed edge tables in SQLite with recursive CTEs remain the choice. The embedded graph database that would have been the alternative, Kuzu, was archived by its creator in October 2025 (its company was acquired by Apple according to a February 2026 filing) and its community is split across forks, of which LadybugDB is the active one. Adopting a fork for a graph of at most a few million edges and two-to-four-hop queries would add a C++ dependency and a fork risk for no measured benefit. This decision has a replacement path: the graph API (`internal/graph`) is an interface with a SQLite implementation; a benchmark in `evals/graph/` compares traversal latency and can justify a swap later.

---

## 4. Docker-first installation and operation

### 4.1 Distribution

Default: one image, one container, one persistent volume.

```
docker run -d --name local-engineer \
  --gpus all \                                   # omit on CPU-only hosts; the image detects and falls back
  -v le-data:/data \                             # workspaces, models, config, ledger, artifacts
  -v "$HOME/code":/work \                        # your repositories, bind-mounted
  -p 127.0.0.1:7777:7777 \                       # supervisor API and dashboard (loopback only)
  ghcr.io/<owner>/local-engineer:<version>
```

Then `docker exec -it local-engineer le tui` or the OpenCode TUI attached to the container, or `le` from the host talking to `127.0.0.1:7777`. A `docker-compose.yml` wraps the same command with named volumes; `docker-compose.split.yml` offers the multi-container variant (inference in its own container, supervisor in another) for users who prefer conventional practice.

Host prerequisites (the only ones): Docker Engine 24+ (or Podman with the compat socket), and for GPU inference the NVIDIA driver plus the NVIDIA Container Toolkit (or ROCm equivalents). Nothing else is installed on the host. Editor integration uses `opencode acp` executed via `docker exec`, or the ACP-over-TCP bridge shipped in the image [VERIFY that the pinned OpenCode release supports ACP over a socket; otherwise `docker exec` is the path].

### 4.2 Image contents and variants

- `local-engineer:<v>` (amd64, CUDA runtime, ~4-5 GB [INFERENCE]): `le` supervisor, OpenCode (pinned), llama-server CUDA build (pinned), Go toolchain, Node LTS, gopls, staticcheck, golangci-lint, govulncheck, gosec, semgrep, gitleaks, osv-scanner, trivy, squawk, buf, oasdiff, benchstat, ripgrep, git, bubblewrap, Playwright browsers (optional layer).
- `local-engineer:<v>-cpu` (amd64 and arm64): same without CUDA; for Apple Silicon and CPU-only hosts, inference is expected to be external (Section 9).
- `local-engineer:<v>-slim`: supervisor plus OpenCode only; inference and toolchains external. For users bringing their own provider.

Images are built from a pinned base, with SBOM and provenance attached, and every tool version listed in `docs/reference/image-manifest.md` generated at build time.

### 4.3 Process model inside the single container

`le` is the entrypoint (PID 1 via `tini`, or Docker `--init`). It supervises three long-lived children with health checks, restart with backoff, and orderly shutdown on `SIGTERM`:

1. `llama-server` (only when `inference.mode: embedded`), started with the active profile from `models.ini`.
2. `opencode serve` per active task, inside the task sandbox (Section 6).
3. `le api` (HTTP on 7777): supervisor API, dashboard, ACP bridge.

Trade-off, stated: one-process-per-container is the conventional practice because it gives the orchestrator (compose, Kubernetes) visibility into each process. Here the supervisor already is a process manager with a ledger; putting the children under it keeps one lifecycle and one health endpoint. The split compose file exists for anyone who wants the conventional layout, and the supervisor's child management is behind an interface so an external `llama-server` is a configuration change, not a code change.

### 4.4 Lifecycle

- Start: `docker run` or `docker compose up -d`; `le` migrates `/data` schemas forward, validates config, probes GPU, admits the model profile by measured peak memory, starts children, reports readiness on `/healthz` and `/readyz`.
- Stop: `SIGTERM` leads to pausing running tasks with a handoff record (v2.0 Section 11.4), aborting sessions, terminating sandboxes, flushing SQLite WAL, then exit. A 30 s grace period is documented; a longer one can be configured.
- Update: pull a new image; `le` runs forward-only schema migrations with a pre-migration backup of `/data` databases (`le backup` uses SQLite's online backup API); downgrades are not supported across schema versions and the docs say so.
- Back up: `le backup --to /data/backups/<ts>.tar` or bind the volume; restore is documented.
- Uninstall: remove the container and the volume.

### 4.5 Documentation promises (checked in CI by a docs test that runs the commands)

Prerequisites, installation, configuration, model configuration, persistent storage, start, stop, update, troubleshooting, example workflows: each is a how-to page with a copy-paste command block and an expected-output block that CI executes against the CPU image.

---

## 5. Storage architecture

### 5.1 Requirements and candidates

Workloads: structural queries (symbol lookup, references, two-to-four-hop traversals), lexical search, optional vector search over 10^4 to 10^6 chunks, a transactional task ledger with an append-only journal, telemetry, and content-addressed artifacts. Constraints: single container, persistence on a volume, no separate database process by default, low query latency for interactive use, reproducible backups.

| Candidate | Fit | Verdict |
|---|---|---|
| SQLite (WAL) with FTS5 and recursive CTEs, one file per concern per workspace | Embedded, transactional, zero-process, backup API, mature; recursive CTEs handle shallow traversals well; FTS5 built in | **Chosen** |
| PostgreSQL inside the container | Excellent engine; requires a second daemon, its own init, upgrades and backups; overkill for single-user local | Rejected for the default; the split compose file can use it later if a server mode ever exists |
| DuckDB (with DuckPGQ for graph queries) | Analytical, columnar; weak fit for the transactional ledger and many small writes | Rejected; optional for offline analytics over telemetry |
| Kuzu / LadybugDB (embedded graph) | Native Cypher and joins; upstream archived, fork ecosystem unsettled; C++ dependency | Rejected now; interface allows revisiting |
| sqlite-vec | Vector index inside SQLite; pre-1.0 (0.1.7 alpha line), long gaps between releases, a community fork exists; recent releases add IVF and DiskANN | Optional extension for large chunk counts; brute-force in Go is the default |
| LanceDB / Qdrant / Milvus | Vector engines; daemons or heavy embedded dependencies | Rejected |
| Badger / bbolt for the journal | Fast key-value; no SQL for reporting | Rejected; the ledger benefits from SQL |

### 5.2 Layout per workspace

```
/data/
  config/            le.yaml, models.ini, providers.yaml, policies/
  models/            GGUF files (or a bind mount to an existing model directory)
  workspaces/<id>/
    index.db         files, nodes, edges, chunks, chunks_fts, index_keys, embeddings (BLOB)
    ledger.db        requirements, tasks, task_deps, operations (intent/outcome journal), checkpoints, evidence, handoffs, leases
    telemetry.db     events, gpu_samples
    artifacts/       content-addressed
    cache/           package loads, TS programs, doc notes
    opencode/        XDG dirs for the engine
    slots/           llama-server slot saves
    tmp/
  backups/
```

Three files rather than one so that a large index rebuild never blocks the ledger, and so that the ledger can be backed up independently at high frequency.

### 5.3 Performance evidence plan

`evals/storage/` holds a benchmark that builds a synthetic graph at 10^5 and 10^6 edges and measures: symbol lookup p50/p95, 3-hop caller traversal, FTS query, brute-force vector scan at 10^5 chunks by 1024 dims, ledger append and checkpoint latency. Expected on this laptop [INFERENCE]: lookups under 1 ms, 3-hop traversals under 50 ms at 10^6 edges, vector scan 10-30 ms at 10^5 chunks. Results are committed to `docs/benchmarks/results/` with hardware disclosure and become the evidence for keeping or replacing SQLite for any component.

### 5.4 Reliability in a container

SQLite requires a real filesystem with working `fsync`; the volume must be a local disk or a named volume, not an overlay layer and not a network share (documented). WAL mode with `synchronous=NORMAL` for index and telemetry, `synchronous=FULL` for the ledger. Journal writes use `BEGIN IMMEDIATE`. Integrity check on startup (`PRAGMA quick_check`), and `le doctor` runs `integrity_check` on demand.

---

## 6. Isolation inside the container

### 6.1 Layers

The v2.0 design used host bubblewrap with user namespaces. Inside a Docker container, unprivileged user namespaces are commonly unavailable and the container runtime's seccomp profile may block namespace creation. The design becomes three layers, each documented with what it does and does not guarantee:

1. **Container boundary** (always): the container itself, running as an unprivileged user (`le`), with no Docker socket, no host credential mounts, read-only root filesystem where possible, `--cap-drop ALL`, and only the bind mounts you give it. This bounds the whole system, including tests and builds, to the mounted repositories and the data volume.
2. **Landlock per task** (default inside the container) [VERIFIED kernel docs; go-landlock]: the sandbox runner restricts each task process tree, unprivileged, to read-only toolchain paths, read-write on its worktree, tmp and caches, and TCP connect only to the inference proxy and assigned test-service ports (network restrictions need kernel 6.7+, ABI v4; the Go library targets up to ABI v10 with best-effort degradation; Landlock's network coverage is documented as incomplete, so it is augmented, not relied on alone). Docker's default seccomp profile must permit the three Landlock syscalls [VERIFY on the target runtime; document the flag to add if not].
3. **Bubblewrap per task** (optional, when the container runs with `--security-opt seccomp=unconfined --security-opt apparmor=unconfined` and user namespaces, or on bare-metal installs): adds mount and PID namespaces on top of Landlock, restoring the v2.0 guarantees. `le doctor` reports which layers are active.

Egress: the container is started with a user-defined bridge network and, in fully offline mode, `--network none` plus an in-container inference route only. For the `deps` and `docs` lanes (v2.0 Section 6.3) an allowlisting proxy runs inside the container and is the only route out, and only for the provisioning lane, never for a task sandbox.

### 6.2 What is guaranteed at each layer

| Guarantee | Container only | + Landlock | + bwrap |
|---|---|---|---|
| Cannot touch host files outside mounts | yes | yes | yes |
| Cannot read another workspace's data | by file permissions and per-task Landlock rules | yes (paths outside the task set are denied) | yes |
| Cannot reach model-management endpoints | via proxy allowlist only | yes (TCP port rules) | yes |
| Cannot see other tasks' processes | no | no | yes (PID namespace) |
| Out-of-scope writes in the worktree | detected by diff | detected by diff | detected by diff |
| Cannot modify policy, ledger, hidden tests | file permissions and Landlock | yes | yes |

The honest statement for the README: "the default container gives strong isolation from your host and between workspaces; process-level isolation between concurrent tasks requires the optional namespace mode".

---

## 7. Crash-safe and interrupt-resilient execution

v2.0 Sections 8.5 and 11.4 define the crash protocol, leases and handoff. This section turns them into a concrete journal so that the recovery requirements in the brief are met exactly.

### 7.1 Execution journal (`ledger.db`)

```sql
CREATE TABLE operations (
  id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, seq INTEGER NOT NULL,
  kind TEXT NOT NULL,          -- inspect_file, search, retrieval, decision, edit, recipe_run, review, checkpoint, approval, session_start, session_end
  intent JSON NOT NULL,        -- written BEFORE the side effect
  outcome JSON,                -- written AFTER; NULL means uncertain
  candidate_before TEXT, candidate_after TEXT,   -- content manifest hashes
  evidence_id TEXT, started_at INTEGER, finished_at INTEGER,
  UNIQUE (task_id, seq)
);
CREATE TABLE checkpoints (task_id TEXT, seq INTEGER, state TEXT, handoff JSON, candidate TEXT, created_at INTEGER);
CREATE TABLE leases (worktree_id TEXT PRIMARY KEY, task_id TEXT, holder TEXT, expires_at INTEGER);
```

Every model-visible action is an operation. Inspections (which files were read, which symbols queried), decisions (accepted hypotheses, rejected hypotheses with evidence ids), edits (with before and after hashes), recipe runs (with evidence), reviews and approvals are all journaled with intent first and outcome second.

### 7.2 Recovery procedure

On start, for every task not in a terminal state:
1. Reconcile: compare the worktree's content manifest with `candidate_after` of the last completed operation; classify any operation with NULL outcome as uncertain and inspect (file exists and matches intent hash: mark complete; partially matches: mark partial; absent: mark not applied).
2. Reconstruct the working state from the last checkpoint plus completed operations: objective (requirement), investigated files and symbols, decisions, completed edits, validations already run (evidence ids and their statuses against the current candidate hash; evidence for an older candidate is marked stale), remaining plan steps, and the next action.
3. Resume from the next action with a fresh session seeded by the handoff; never replay a recipe or an edit whose outcome is uncertain without re-checking the candidate.
4. Lease expiry: a crashed holder's lease expires; a new instance takes it after reconciliation. Two instances never write the same worktree.

Stage D recovery tests (v2.0 Section 17.1) verify each interruption class: application restart, model failure, user interruption, system crash (kill -9), process termination, hardware failure (simulated by dropping the inference route), timeout, context restart.

---

## 8. Making the context window effectively irrelevant

### 8.1 Principle

The model never needs the whole repository in context because every question about the repository is answered outside the model first (graph, index, journal), and only the answer's supporting slices enter the packet. Small windows are treated as an advantage: they force precise retrieval and keep irrelevant text out.

### 8.2 Techniques adopted, with the evaluation that justifies each

| Technique | What it solves | Evidence | Cost | Adopted as |
|---|---|---|---|---|
| Hierarchical context (system map, task neighbourhood, working evidence) | keeps orientation without whole-repo text | Aider repo map; Anthropic context-engineering guidance; LocAgent's hierarchy | low | v2.0 Section 9.1 |
| Lexical anchors then graph expansion | localization for small models | LocAgent, RepoGraph, LARGER | index cost | retrieval order in v2.0 Section 9.3 |
| Dependency- and impact-aware retrieval | consumers and contracts never dropped | our impact analysis | low | mandatory slots in packets |
| Just-in-time tool reads with paging | avoids preloading | SWE-agent ACI | none | tools in v2.0 Section 5.4 |
| Progressive disclosure (signatures first, bodies on demand) | small packets | Aider; RLM bounded inspection | none | packet builder |
| Structured handoffs instead of chat summaries | long tasks across sessions | Anthropic harness guidance; our journal | none | v2.0 Section 11.4 |
| Durable external memory split into intent, observation, advice | prevents stale rules and self-praise from steering | ACE-style provenance | low | v2.0 Section 11 |
| Cache-aware packet layout | prefill cost | llama.cpp prompt cache (you measured 99.8% reuse) | none | stable prefix first |
| Bounded recursive inspection (depth 1, small fan-out) | large logs and repos | RLM paper; our bounded tools | model calls | optional, Phase 6 |
| Compression of tool output at source (structured summaries of test and compiler output) | token waste | deterministic | none | recipe summarizers |
| Semantic retrieval | recall when names are unknown | mixed evidence for code | embedding cost | optional; enabled only if Stage E shows benefit |

Rejected: whole-repository summaries generated up front (cost without measured value); "context compression" by asking the model to summarize its own history (loses facts; replaced by the journal); ever-larger windows (prefill cost on this hardware, and diminishing attention quality).

### 8.3 Measurement

Context-retrieval misses (a needed symbol or consumer absent from the packet, discovered later by a failure) are a primary metric (v2.0 Section 17.2). The needle test sets the hard packet cap per model profile. Packet size per step is charted in the dashboard.

---

## 9. Model-agnostic architecture

### 9.1 The abstraction

Two boundaries, both configuration:

1. **Provider boundary**: the OpenAI-compatible chat, completion, embeddings and infill HTTP surfaces. The engine (OpenCode) speaks to providers through its provider layer; the supervisor's direct calls (schema-constrained reviews, embeddings, classification) go through `internal/llm`, a small client with a `Provider` interface: `Chat`, `ChatStructured(schema)`, `Embed`, `Infill`, `Capabilities()`.
2. **Role routing**: `providers.yaml` maps roles (orchestration, planning, coding, repository search, summarization, review, classification, reranking, verification) to a provider and model alias, with a default that maps all roles to one model. Multi-model routing is off by default and turned on per role only with Stage E evidence.

Supported provider kinds at 1.0: `llamacpp` (embedded or external URL), `openai_compatible` (any URL: vLLM, SGLang, Ollama, LM Studio, TGI, cloud gateways), `anthropic` and `openai` (remote, opt-in, off in offline mode). Adding a provider is one Go file implementing `Provider` plus a `Capabilities` declaration (tool calling, structured output, vision, infill, max context, cost).

### 9.2 Hardware profiles as configuration

`profiles/` ships named profiles, each a `models.ini` fragment plus context and admission limits, generated from Phase 0 measurements and reproducible with `le models bench`:

- `reference-8gb-cuda-64gb-ram` (your laptop: MoE with CPU experts).
- `resident-8gb-cuda` (a 9B-class dense model fully on GPU).
- `cpu-only-32gb-ram` (small dense model, reduced packets).
- `external-inference` (no embedded server).
- `remote-provider` (cloud model, offline lanes disabled).

`le models bench` runs the Stage B throughput and admission measurements on the user's machine and writes a profile; `le doctor` warns when the active profile's measured memory does not fit the host.

### 9.3 What must not be hardcoded

Context limits, VRAM and RAM budgets, thread counts, offload layers, sampling defaults, thinking policy, tool-surface size, packet sizes. All of these live in the profile and the workspace config, and the reference profile is only the default value.

---

## 10. Compensating for limited model intelligence

### 10.1 Technique evaluation

| Technique | Problem | Evidence | Resource and latency cost | Complexity | Decision |
|---|---|---|---|---|---|
| Decomposition into bounded child tasks with executable acceptance | long tasks, drift | Agentless; Anthropic harness guidance; our own Stage C | none | medium | adopted (v2.0 Section 8) |
| Compiler, vet, lint, test, race feedback loops | most first-attempt errors | universal in SWE-agent-class systems | seconds per loop | low | adopted; the core |
| Static-analysis and project-invariant analyzers (`go/analysis`, semgrep) | domain rules the model does not know | deterministic | seconds | medium | adopted |
| Runtime feedback (browser, disposable DB, integration) | "compiles but wrong" | our verification levels | minutes | high | adopted for HIGH and UI |
| Constrained generation and structured outputs | malformed plans and verdicts | llama.cpp grammars | none | low | adopted for direct calls; validated in Go for engine calls |
| Code-graph grounding in retrieval | wrong files | LocAgent, RepoGraph | index cost | medium | adopted |
| Fresh-context review and critique | self-consistent errors | hypothesis; Stage E | one extra call per task | low | adopted as measured |
| Fresh diagnosis after repeated failure | stuck hypotheses | our failure classes | one call | low | adopted |
| Candidate generation and ranking (two candidates from the same baseline, judged by acceptance evidence) | real ambiguity | bounded search; evidence-judged, not model-voted | 2x generation | low | adopted, sparingly |
| Model routing (stronger slow lane for diagnosis) | hard bounded problems | measured per task class only | load time | medium | optional |
| Specialized sub-agents (personas) | none demonstrated for same-model personas | negative | context loss | medium | rejected as personas; kept as context recipes |
| Reflection without new evidence ("think harder") | none | negative evidence in our own loops | tokens | low | rejected |
| Deterministic generation for boilerplate (sqlc, buf, OpenAPI clients) | avoidable model output | project practice | none | low | adopted; contract checks still apply |
| Persistent cross-attempt state (rejected hypotheses, evidence) | repeated mistakes | "Persistent Cross-Attempt State Optimization" (2026), our journal | none | low | adopted |

### 10.2 The distinctive claim, and how it is proven

The README will claim only what `docs/benchmarks/results/` shows: for each task category, the accepted-task rate within a fixed budget for (a) the same local model through plain OpenCode, (b) the supervised system, (c) a frontier agent on a non-sensitive set. The benchmark methodology, task set, hardware, model manifest and scripts are published so anyone can reproduce or dispute the numbers.

---

## 11. Latest AI engineering advances considered (to 14 September 2026)

| Development | Assessment | Use here |
|---|---|---|
| 3B-active MoE models with CPU expert offload in llama.cpp | The single biggest practical change for 8 GB GPUs | reference profile candidates (v2.0 Section 3.1) |
| Coding-specialised sparse models (Qwen3-Coder-Next, Laguna XS) | promising; memory and runtime support vary | Phase 0 candidates |
| Native vision in small models (Qwen3.5/3.6, Gemma 4) | removes a second model for UI review | Phase 6 |
| Multi-token-prediction drafts (Gemma 4) and DFlash drafts (Laguna) | speed, if accepted-token rates are good | measured, not assumed |
| Agent Client Protocol adoption in editors | editor integration for free through OpenCode | adopted |
| Built-in gopls MCP tools | compiler-backed symbol tools without custom code | adopted, detached mode |
| Graph-guided localization (LocAgent, RepoGraph, LARGER) | strong evidence for structure over pure embeddings | adopted |
| Repository memory from commit history (RepoMem) | cheap extra signal | `git_touch`, blame tools |
| Agentic Context Engineering (playbooks with provenance) | good pattern, easy to abuse | lessons with provenance and caps |
| Recursive Language Models | useful pattern for big logs; not proven for small local models | bounded depth-1 variant, optional |
| Durable-execution patterns (LangGraph persistence, workflow engines) | right semantics, wrong dependency weight | reimplemented as the journal |
| Harness design for long-running agents (Anthropic) | generator and evaluator separation, structured handoffs | adopted |
| Training-environment approaches (Libra, SWE-smith style data generation) | relevant to fine-tuning, out of scope | noted for a future "fine-tune a localizer" experiment |

---

## 12. Decision records (seven-point format)

Each record is also stored as `docs/adr/NNNN-*.md` in MADR layout.

### DR-1: Single-container distribution with SQLite storage
1. Problem: users must run the system with one command and keep data across restarts.
2. Alternatives: multi-container compose (app, database, inference); single container with an embedded PostgreSQL managed by s6-overlay; single container with SQLite.
3. Evidence: SQLite is embedded, transactional and has an online backup API; the workloads are single-user and small; PostgreSQL-in-container needs a second init system and upgrade handling; the split compose file preserves the conventional option.
4. Chosen: single container, SQLite files per workspace, `le` as the process supervisor.
5. Why: simplest reproducible install; one volume to back up; no daemon lifecycle inside the container.
6. Disadvantages: multiple processes under one PID 1 reduce orchestrator visibility; SQLite needs a real filesystem; no server mode for multiple users.
7. Replacement: storage behind `internal/store` interfaces; the split compose variant is maintained and tested; a PostgreSQL implementation can be added for a future server mode.

### DR-2: Graph in SQLite edge tables rather than an embedded graph database
1. Problem: a queryable code graph with 2-4 hop traversals and impact analysis.
2. Alternatives: Kuzu or a fork (LadybugDB); DuckDB with DuckPGQ; Neo4j; SQLite recursive CTEs.
3. Evidence: Kuzu archived October 2025 with a fragmented fork ecosystem; graph sizes here are at most millions of edges; recursive CTEs are adequate for shallow traversals; a storage benchmark is part of the repo.
4. Chosen: SQLite edge tables with evidence categories and recursive CTEs.
5. Why: no new dependency, no fork risk, one storage technology, trivially isolated per workspace.
6. Disadvantages: no Cypher; deep or analytical graph queries are slower; adjacency updates are manual.
7. Replacement: `internal/graph` interface; benchmark harness in `evals/graph/` can justify a swap.

### DR-3: Container boundary plus Landlock, bubblewrap optional
1. Problem: isolate task execution inside a container without privileges.
2. Alternatives: host bwrap (v2.0); gVisor or Kata; Landlock; Docker-in-Docker sibling containers; no per-task isolation.
3. Evidence: Landlock is unprivileged, in mainline since 5.13, with TCP restrictions from 6.7 and a maintained Go library; user namespaces are often unavailable inside containers; the Docker socket must not be mounted.
4. Chosen: three layers with `le doctor` reporting which are active.
5. Why: works in the default `docker run`; degrades gracefully; strongest mode still available.
6. Disadvantages: without namespaces, concurrent tasks share a PID view; Landlock's network coverage is incomplete; the seccomp profile must allow Landlock syscalls.
7. Replacement: the sandbox runner is an interface; a gVisor or microVM runner can be added for untrusted repositories.

### DR-4: OpenAI-compatible HTTP as the provider boundary
1. Problem: model and backend independence.
2. Alternatives: native SDK per vendor; a custom RPC; OpenAI-compatible HTTP plus a small capability declaration.
3. Evidence: llama.cpp, vLLM, SGLang, Ollama, LM Studio and cloud gateways expose it; OpenCode's provider layer consumes it.
4. Chosen: OpenAI-compatible HTTP plus `Capabilities()`; grammar-constrained calls only for providers that advertise them.
5. Why: broadest reach with the least code.
6. Disadvantages: feature gaps between providers (thinking control, infill, grammars) are hidden behind one API and must be declared explicitly.
7. Replacement: `Provider` interface; a native provider can implement it directly.

### DR-5: OpenCode as the execution engine (carried from v2.0)
Problem, alternatives, evidence, choice, disadvantages and replacement path are in v2.0 Sections 4 to 5; the replacement threshold is v2.0 Section 5.6.

### DR-6: Workspace identity as the isolation key
1. Problem: never confuse projects.
2. Alternatives: path-keyed directories; git-remote-keyed; explicit id file.
3. Evidence: paths move, remotes change; an explicit pinned id with an adopt command survives both.
4. Chosen: hash-derived id pinned in `.le/workspace.yaml`, separate database files per workspace, storage API scoped by handle.
5. Why: isolation enforced by file separation and by type, not by discipline.
6. Disadvantages: duplicate indexing if a repository is used in two workspaces; an id file to keep in the repo.
7. Replacement: the id scheme is versioned; a migration command can re-key.

---

## 13. Repository layout of the system (delta from v2.0 Section 20)

Added: `internal/workspace` (identity, adoption, isolation tests), `internal/store` (single entry point for all persistence), `internal/graph` (interface plus SQLite implementation), `internal/llm` (provider interface and implementations), `internal/procman` (child process supervision for the single container), `internal/sandbox/landlock` and `internal/sandbox/bwrap`, `entrypoint/`, `deploy/` (Dockerfile variants, compose files, Helm chart later), `profiles/`, `docs/` in Diátaxis layout, `evals/storage/` and `evals/graph/`, `examples/`.

Removed relative to v2.0: nothing; host-mode installation remains supported as `scripts/install-bare-metal.sh` for developers but is not the documented default.

---

## 14. Phase plan adjustments

The v2.0 seven-phase plan stands with these additions:

| Phase | Added scope | Added exit condition |
|---|---|---|
| 0 | Repository scaffold with all governance files, CI gates, CPU image build, `le models bench` | Scorecard runs on `main`; CPU image starts and passes `/readyz`; docs commands execute in CI |
| 1 | Workspace identity, `internal/store` scoping, Landlock runner, single-container process model, isolation tests | Two-workspace isolation test passes; DR-3 layers reported by `le doctor`; first bug fix completes inside the container |
| 2 | Execution journal, recovery procedure, backup and restore | Stage D interruption classes all recover; `le backup` and restore round-trip verified |
| 4 | Graph coverage table in Section 3.2 complete for Go; storage and graph benchmarks published | `docs/benchmarks/results/` has the first storage and graph numbers |
| 6 | Provider implementations beyond llama.cpp; profiles for other hardware; split compose variant tested | A second provider passes Stage A; a CPU-only profile runs the tutorial end to end |
| Release 1.0 | All of the above plus the v2.0 Section 18 matrix | Signed release with SBOM, provenance and `BENCHMARKS.md` |

---

## 15. Open-source readiness checklist (tracked as issues)

- [ ] Name, licence (Apache-2.0), NOTICE with third-party attributions (OpenCode MIT, llama.cpp MIT, model licences listed per profile)
- [ ] README with a 60-second demo (asciinema), architecture diagram, honest limitations, hardware table
- [ ] CONTRIBUTING, CODE_OF_CONDUCT, SECURITY (private reporting), SUPPORT, GOVERNANCE, MAINTAINERS, CODEOWNERS, ROADMAP, CITATION.cff
- [ ] Issue forms including a "model report" form (model, quant, profile, Stage A results)
- [ ] CI: lint, vet, staticcheck, race tests, sidecar tests, schema and policy validation, integration in container, short fuzz, CodeQL, govulncheck, osv-scanner, commit-lint, docs execution test
- [ ] Scorecard workflow and badge; Best Practices badge application after 1.0
- [ ] Release: GoReleaser, SemVer tags, Conventional Commits changelog, SLSA provenance, Sigstore signatures, SBOMs, multi-arch images with digests
- [ ] Dependabot or Renovate with grouped updates; actions pinned by SHA
- [ ] Dev Container; `make check` equals CI
- [ ] Docs site (Diátaxis), C4 diagrams, ADRs (DR-1 to DR-6 and v2.0 decisions), benchmark methodology and results
- [ ] Examples: Go microservice, Nuxt app, monorepo with a catalog
- [ ] Troubleshooting page generated partly from `le doctor` checks

---

## 16. Verify list added by v3.0

1. Landlock syscalls under the target container runtime's default seccomp profile; behaviour under Podman.
2. Whether unprivileged user namespaces are available inside the default container on Ubuntu 26.04 hosts (decides whether the bwrap layer is on by default).
3. `opencode acp` transport options for editor integration from outside the container.
4. NVIDIA Container Toolkit and CUDA runtime version pairing for the pinned llama.cpp build; ROCm image feasibility.
5. Image size after layering; whether Playwright browsers belong in a separate layer or image.
6. SQLite performance on the Docker named volume versus a bind mount on the reference laptop (benchmark in `evals/storage/`).
7. sqlite-vec loadable-extension support in the chosen Go SQLite driver if it is ever enabled.

---

## 17. Sources added by v3.0

Open-source practice: OpenSSF Scorecard repository and v5 release notes; OpenSSF Concise Guide for Developing More Secure Software; OpenSSF Best Practices badge; slsa-github-generator; hardened reusable Go workflow examples (2026); GoReleaser documentation.
Storage: The Register on Kuzu's archival (October 2025); Kuzu fork landscape summaries (October 2025, updated August 2026); PuppyGraph on Kuzu status (June 2026); sqlite-vec releases and the community fork.
Isolation: Linux kernel Landlock documentation (January 2026); `landlock-lsm/go-landlock`; `landrun`.
Research: LocAgent (ACL 2025); RepoGraph (ICLR 2025); CodexGraph; Agentless; SWE-agent; LARGER (May 2026); "Code Isn't Memory" (June 2026); RepoMem (October 2025); "Persistent Cross-Attempt State Optimization" (April 2026); Libra (July 2026); Awesome-Repo-Level-Code-Generation list.

End of document.
