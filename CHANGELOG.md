# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Released sections are generated from Conventional Commits at release time.

## [Unreleased]

### Added

- **Workspace identity and isolation contract.** A workspace id derived from
  the canonical root, the git remote and a user-supplied name, pinned in
  `.le/workspace.yaml`, with `le workspace adopt` to re-bind after a move.
  Every subsystem stores state under `$LE_DATA/workspaces/<id>/` and nowhere
  else; the storage API cannot be reached without a workspace handle.
- **`storescope` analyzer** enforcing that `sql.Open` and file writes stay
  inside `internal/store` and `internal/artifacts`, so isolation is a build
  failure rather than a convention.
- **Per-workspace SQLite storage**: `index.db`, `ledger.db` and `telemetry.db`
  with forward-only migrations, WAL mode, `synchronous=FULL` for the ledger,
  startup `quick_check`, and a workspace stamp that refuses to open a database
  belonging to another workspace.
- **Typed code graph** with five evidence categories and recursive-CTE
  traversal, plus `impact_of` reporting consumers with deterministic
  compatibility verdicts and migration steps.
- **Retrieval** in the designed order — lexical anchors, then graph expansion,
  then mandatory impact slots — with a guard that rejects any slice from
  another workspace.
- **Execution journal and recovery**: intent written before the side effect and
  outcome after, uncertain operations classified by inspecting the worktree,
  evidence marked stale against an older candidate, and worktree leases that
  expire so a crashed holder's claim is reclaimable.
- **Three-layer sandbox** (container, Landlock, optional bubblewrap) with a
  runner interface, honest per-layer guarantees, and `le doctor` reporting
  which layers are actually active.
- **Process manager** for the single-container process model, with health
  checks, restart budgets with backoff, process-group termination and an
  orderly SIGTERM path.
- **Provider abstraction** over OpenAI-compatible HTTP plus an explicit
  capability declaration, with llama.cpp, generic OpenAI-compatible, OpenAI and
  Anthropic providers, role routing, and offline mode refusing remote providers
  at startup.
- **Hardware profiles as configuration** and `le models bench` to measure a
  machine and generate one.
- **Supervisor API** with `/healthz`, `/readyz`, `/v1/status`, `/v1/sandbox`
  and a dependency-free dashboard.
- **Container images** (slim, cpu, cuda targets), default and split compose
  files, and an entrypoint that reports the active isolation layers before
  starting.

- **Go language analyzer** producing the compiler-backed rows of the coverage
  table: modules, packages, imports, the call graph, interface satisfaction via
  `types.Implements`, type usage, signatures, struct fields and embedding,
  tests, configuration keys and HTTP routes. Interface dispatch is
  over-approximated with class hierarchy analysis and labelled `inferred` with
  the assumption recorded on the edge, so impact analysis reports those
  consumers as undetermined rather than making a claim it cannot support.
- **Commit-history analyzer** contributing `observed` commit-to-file edges,
  with sweeping commits skipped because a vendor drop says nothing about
  coupling.
- **Per-task git worktrees**, so a task never edits the operator's checkout,
  and `SyncFrom` to carry uncommitted work into one deliberately.
- **Verification recipes** for build, vet, test, race and format, each with a
  summariser that compresses output at source: the full log becomes a
  content-addressed artifact and only the findings travel.
- **Verification levels and the completion contract.** A task is accepted only
  when every recipe its level requires has a passing result produced against
  the current candidate, with no out-of-scope writes. A skip, an error, or a
  pass against an older candidate satisfies nothing.
- **Engine adapter contract** (`internal/engine`) with a verification-only
  implementation, so the whole pipeline runs and is tested without a model.
- `le task verify`, `le task create` and `le task run`.

- **Tool calling in the provider layer**, across both wire formats: the
  OpenAI-compatible function envelope and the Messages API's content blocks,
  including replaying tool calls on assistant turns and merging tool results
  into a single user turn.
- **The editing engine** (`internal/engine/native`): a bounded tool loop with
  nine tools — read, edit, write, list, search, find symbol, impact, run
  verification, done. Every file operation is confined to the task worktree by
  `internal/worktree` as well as by the sandbox, tested against traversal and
  symlink escapes. A failed tool call is a result the model can act on, not an
  error that discards the turn.
- **Task decomposition** into bounded child tasks with dependencies. A plan is
  produced by a model and validated by the supervisor: a step with no scope, a
  scope escaping the repository, an unknown verification level, or a forward
  dependency is rejected before anything runs.
- **The broker and human gates.** A gate carries the deterministic evidence —
  impact report, diff, verification findings — and is journalled before it
  blocks, so an interrupted approval is a pending gate on restart rather than a
  lost one. Which decisions gate is configuration; a budget increase always is.
- `le plan`, `le gate list/show/approve/reject`.

- **Schema analyzer**: a pure-Go PostgreSQL DDL parser over migrations,
  applied in file order so the result is the schema as it ends up. Statements
  outside the subset are counted and reported rather than skipped silently.
- **Schema to application code**: type-checked database call sites, so a table
  change finds the queries that read it. A literal query is `resolved`; a
  wrapper driver matched by method name is `inferred`; a query assembled at
  runtime names no table at all.
- **Deployment and build analyzers**: compose services and their environment,
  Kubernetes workloads and ConfigMaps, Dockerfile stages and `COPY` sources,
  Makefile targets. Helm templates are recorded as present but not parsed,
  because rendering needs chart values.
- **Terraform analyzer**: resources, modules, variables and the references
  between them, plus the environment variables they set.
- An impact report now crosses analyzer boundaries: changing a configuration
  key finds the Go code, the compose service, the Dockerfile stage and the
  Terraform resource in one traversal.

### Fixed

- **Edge directions that made consumers invisible.** Impact analysis is a
  reverse traversal, so an edge pointing the wrong way is not wrong in any
  visible way — the report simply comes back short. `implements` pointed
  interface → type, so adding a method to an interface reported **zero**
  implementations; `reads_config` pointed key → reader in the Go analyzer but
  reader → key everywhere else, so a config change found the manifests that
  set a variable but not the code that read it. Both corrected, the invariant
  is stated where edge kinds are defined, and a cross-analyzer test guards it.
- **`#` or `?` in a data-directory path silently opened the wrong database.**
  The SQLite DSN concatenated the path into a `file:` URI, where `#` starts a
  fragment and `?` starts a query — so the path was truncated, and two
  workspaces differing only after that character resolved to the *same file*.
  Both opened successfully, so nothing downstream could detect it. The path is
  now escaped into the URI.
- **Analyzer output order was load-bearing.** Edges were resolved per analyzer,
  so a cross-analyzer edge was dropped unless its target happened to be written
  first. The indexer now runs every analyzer, writes every node, then resolves
  every edge.

- **Evaluation harness** (`internal/eval/`, `evals/tasks/`, `le eval`):
  - Acceptance tests are never in the worktree while a task runs. A model that
    can read the test can satisfy it without solving the problem, and that
    failure looks exactly like success in the numbers.
  - The system's own verdict is recorded separately from the ground truth, so
    "claimed success and was wrong" is its own rate rather than something
    averaged away.
  - Protected paths are checked: a task passed by deleting the failing test is
    counted as unsolved.
  - A run that errors is excluded from every rate, because a harness fault is
    not evidence about the system.
  - Arms for the unsupervised baseline, the full system, and ablations of the
    graph and of the verification loop — structurally different pipelines, not
    flags the system might ignore.
  - Wilson confidence intervals on every rate, and a difference called
    significant only when the intervals do not overlap.
  - Task-set validation in CI: acceptance must fail on the untouched fixture,
    a reference solution must pass, fixtures must start green, and objectives
    must not name the fix.

### Known gaps

- TypeScript has no analyzer. It needs a Node sidecar for the TS compiler API.
- **No task-success numbers have been produced.** The harness is built and
  tested; a run against a real model on disclosed hardware has not been done.
  Until `docs/benchmarks/results/` contains them, nothing here claims a success
  rate, and the graph's contribution is measurable rather than measured.
- [The graph schema reference](docs/reference/graph-schema.md) marks the state
  per language.
- The design chose OpenCode as the execution engine (DR-5). What shipped is a
  native engine over the provider boundary: the adapter contract is what DR-5
  is about, and a native implementation is testable end to end without an
  external process. An OpenCode adapter remains a second implementation of the
  same interface; the record should be superseded rather than ignored if the
  native engine turns out to be the permanent answer.
