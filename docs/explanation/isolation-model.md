# The isolation model

This page states exactly what is guaranteed, by which layer, and what is not.
Where a guarantee is partial, it says so — a security document that overstates
its case is worse than none.

## Two different kinds of isolation

They are often conflated, and they have different mechanisms:

1. **Isolation between projects** — workspace A must never see workspace B's
   index, cache, sessions or prompt cache. Enforced at the *storage layer*.
2. **Isolation of running code** — a task's build, test and tooling must not
   reach your host or another task. Enforced by the *sandbox layers*.

## 1. Between projects

A **workspace** is the unit. Its id is derived from the canonical root path,
the git remote if any, and a user-supplied name, and pinned in
`.le/workspace.yaml` inside the repository.

Everything lives under that id:

```
$LE_DATA/workspaces/<workspace_id>/
  index.db  ledger.db  telemetry.db  artifacts/  cache/  opencode/  slots/  tmp/
```

Four mechanisms, not one:

**Separate files.** Each workspace has its own SQLite files. There is no
cross-database query anywhere in the codebase, because there is no attached
database to query.

**A scoped API.** The only way to reach storage is
`store.OpenWorkspace(id) → *Store`. There is no exported call that takes a bare
path or a table name. A `workspace.ID` is a distinct type, so it cannot be
confused with an arbitrary string.

**A build-time rule.** A custom `go/analysis` analyzer fails the build if
`sql.Open` or a file write appears outside `internal/store` and
`internal/artifacts`. Exemptions are listed in the analyzer source with the
reason each one is not workspace state. This is why isolation is not a
convention someone can quietly break.

**Row-level stamping and verification.** Every row carries its `workspace_id`,
and a row that surfaces from the wrong handle is refused rather than returned.
A database file records the id of the workspace that created it, so restoring a
backup into the wrong workspace fails at open.

### The cache and the prompt cache

Cache keys mix in the workspace id, so two projects with byte-identical content
compute *different* keys and cannot hit each other's entries.

Prompt-cache slots are cleared on workspace switch. Reuse requires an identical
token prefix, so content could not leak anyway — but the timing and hit
statistics could, and clearing removes that too.

### Retrieval

Every retrieved slice carries `workspace_id`, `repository_id`, `worktree_id`,
path, symbol, content hash and index version. The packet builder rejects any
slice whose workspace differs from the active task, and reports the rejection
rather than dropping it silently.

### There is no global memory

"Cross-project lessons" are an explicit export/import of text you have read,
never an automatic channel. There is no shared semantic store.

### How this is tested

`internal/store/isolation_test.go` indexes two synthetic repositories with
deliberately overlapping file and symbol names — same paths, same function
names, different bodies — and asserts:

- zero rows in either workspace stamped with the other,
- zero cache hits across workspaces for identical content,
- zero cross-workspace slices in any packet, checked by querying for a string
  that exists only in the *other* workspace,
- impact analysis never names a consumer from the other workspace,
- slot files are removed on switch and the engine directories differ,
- a database moved into another workspace's directory is refused at open.

CI additionally *breaks* the cache's workspace binding and requires the suite
to fail. A test that cannot fail protects nothing.

## 2. Of running code

Three layers. `le doctor` reports which are active.

### Layer 1: the container (always)

An unprivileged user, `--cap-drop ALL`, no Docker socket, no host credential
mounts, and only the bind mounts you give it. This bounds everything — tasks,
builds, tests — to your mounted repositories and the data volume.

This is the layer that stops a task reaching your SSH keys, and it is the one
that is always in effect. Running on the host without a container gives it up;
`le doctor` warns when it does not find a container marker.

### Layer 2: Landlock per task (default inside the container)

Each task's process tree is restricted, unprivileged, to read-only toolchain
paths, read-write on its own worktree, tmp and caches, and TCP connect only to
the inference endpoint and assigned test ports.

Landlock restrictions are inherited across `execve` but cannot be applied to
another process, so the runner re-executes `le` with a hidden helper
subcommand: the helper restricts itself, then `execve`s the real command. No
supervising process sits inside the sandbox.

The restriction is best-effort across ABI versions: on an older kernel the
strongest available subset applies, and `le doctor` prints the ABI so the
degradation is visible rather than silent.

### Layer 3: bubblewrap per task (optional)

Adds mount and PID namespaces. This is the only layer that stops concurrent
tasks seeing each other's processes.

It is off by default because unprivileged user namespaces are usually
unavailable inside a container, and on Ubuntu hosts AppArmor blocks them by
default. Enabling it means running the container with
`--security-opt seccomp=unconfined --security-opt apparmor=unconfined`, which
weakens layer 1 — a trade, not a free upgrade.

## The guarantee table

| Guarantee | Container only | + Landlock | + bwrap |
|---|---|---|---|
| Cannot touch host files outside mounts | yes | yes | yes |
| Cannot read another workspace's data | by file permissions and per-task rules | yes | yes |
| Cannot reach model-management endpoints | via proxy allowlist only | yes (TCP port rules) | yes |
| **Cannot see other tasks' processes** | **no** | **no** | **yes** |
| Out-of-scope writes in the worktree | detected by diff | detected by diff | detected by diff |
| Cannot modify policy, ledger, hidden tests | file permissions and Landlock | yes | yes |

The honest summary, which is also in the README and in `le doctor`:

> The default container gives strong isolation from your host and between
> workspaces; process-level isolation between concurrent tasks requires the
> optional namespace mode.

## What is explicitly not guaranteed

**Multipath TCP is not covered.** Landlock's TCP rules do not apply to MPTCP
sockets, and Go's `net.Listen` uses MPTCP by default since Go 1.24. A sandboxed
Go program can therefore still listen on a port the rules do not list. Network
containment is the container's network configuration plus the allowlisting
proxy; the Landlock port rules augment that and are not the boundary. `le
doctor` prints this warning every run.

**Out-of-scope writes inside a task's own worktree are detected, not
prevented.** A task must be able to edit its worktree; the defence is that
every change appears in a diff, not that it was impossible.

**A remote provider sees what you send it.** Choosing one is choosing that.
Offline mode refuses remote providers at startup so it cannot happen by
accident.

**Mounting the Docker socket would defeat all of this.** Nothing here needs it,
and the shipped compose files do not.

## Reporting a problem

If you find a way across a boundary this page claims is closed, see
[SECURITY.md](../../SECURITY.md) — private reporting, not a public issue.
