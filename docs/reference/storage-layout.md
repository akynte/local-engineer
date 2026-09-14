# Storage layout

```
/data/
  config/
    le.yaml            supervisor configuration
    providers.yaml     providers and role routing
    profiles/          profiles you generated (shipped ones are embedded)
    policies/
  models/              GGUF files, or a bind mount to an existing directory
  workspaces/<workspace_id>/
    workspace.json     the data-side record: name, root, last opened
    index.db           files, nodes, edges, chunks, FTS, embeddings, index keys
    ledger.db          requirements, tasks, operations, checkpoints, evidence, handoffs, leases
    telemetry.db       events, GPU samples
    artifacts/<xx>/<hash>   content-addressed evidence, read-only once written
    cache/analysis/    keyed by workspace and content manifest
    opencode/          the engine's XDG data, config and cache
    slots/             saved prompt-cache slots, cleared on workspace switch
    tmp/               wiped at task end; the only tmp a sandboxed task sees
  backups/<timestamp>/<workspace_id>/{index,ledger,telemetry}.db
```

## Why three databases per workspace

A large index rebuild must not block the ledger, and the ledger — the crash
recovery record — should be backed up independently at high frequency.

| File | `synchronous` | Why |
|---|---|---|
| `index.db` | `NORMAL` | Rebuildable from source; speed matters more |
| `ledger.db` | `FULL` | The recovery record; must survive a hard kill |
| `telemetry.db` | `NORMAL` | Counters; a lost sample is not a problem |

All three use WAL mode with a 5-second busy timeout, foreign keys on, and
immediate write transactions so two writers fail fast instead of deadlocking.

## Requirements on the filesystem

SQLite needs a real filesystem with working `fsync`. Not a container overlay
layer, not a network share. `le doctor` fails with exit code 2 on either, and
the entrypoint warns before the supervisor starts.

## How isolation is enforced here

- Separate files per workspace; no cross-database query exists in the code.
- `store.OpenWorkspace(id) → *Store` is the only entry point, and
  `workspace.ID` is a distinct type.
- A build-time analyzer confines `sql.Open` and file writes to
  `internal/store` and `internal/artifacts`.
- Every content row carries its `workspace_id`, and a row surfacing from the
  wrong handle is refused.
- Each database records the workspace that created it, so restoring into the
  wrong one fails at open.

## Content addressing

Artifacts are stored under the SHA-256 of their content, with a two-level
directory fan-out, and set read-only once written. Reads verify the hash, so a
corrupted artifact is never served as evidence.

Cache keys mix the workspace id and the indexer version into the digest, so two
workspaces with identical content compute different keys, and an indexer
upgrade invalidates everything.

## Schema versions

Recorded in each database's `meta` table. Migrations are forward-only: a
database from a newer build is refused with a message pointing at the backup
and upgrade path rather than being misread.

## Sizing

The index is roughly proportional to source size — low tens of megabytes for a
100k-line repository. The ledger grows with task history. Artifacts grow with
verification runs but deduplicate by content.

```console
$ le doctor --json | jq '.checks[] | select(.name | startswith("index"))'
```
