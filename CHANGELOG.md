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

### Known gaps

- The task-execution loop is not implemented; `le task` inspects and recovers
  journals but does not yet run tasks.
- Language analyzers beyond the filesystem and containment layer are not
  implemented, so the graph currently holds `contains` edges only.
- No benchmark results are published yet, and the README makes no performance
  claims until they are.
