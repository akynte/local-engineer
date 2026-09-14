# CLI reference

```
le [global flags] <command> [flags]
```

## Global flags

| Flag | Default | |
|---|---|---|
| `--data <dir>` | `$LE_DATA`, then `/data` | The data directory |
| `--log-json` | off | Structured JSON logs |
| `-v, --verbose` | off | Debug logging |
| `-q, --quiet` | off | Errors only |

## Environment

| Variable | |
|---|---|
| `LE_DATA` | Data directory |
| `LE_API_ADDR` | Overrides `api.addr`. The image sets `0.0.0.0:7777` |
| `LE_PROFILE` | Overrides the active hardware profile |
| `LE_OFFLINE` | `1` or `true` forces offline mode |
| `LE_IN_CONTAINER` | Set by the image; used for container detection |

---

## `le version`

Build, schema, indexer and workspace-id-scheme versions. `--json` for machine
output.

## `le doctor`

Reports what is actually in effect: isolation layers, the data directory's
filesystem, index freshness, profile fit, and stale leases.

| Flag | |
|---|---|
| `--json` | Machine-readable; contains no repository content |
| `--deep` | Full `PRAGMA integrity_check` (slow on a large index) |

**Exit status:** `0` all clear, `1` warnings, `2` failures.

## `le workspace`

| Command | |
|---|---|
| `init [path]` | Create `.le/workspace.yaml` and register the workspace |
| `adopt [path]` | Re-bind the pinned id after the directory moved |
| `list` | Every workspace known to this data directory |
| `show` | The workspace containing the working directory |

`init` flags: `--name` (default: the directory name), `--repo` (repeated, for a
multi-repository workspace), `--force`.

Commit `.le/workspace.yaml`. It pins the identity to the code.

## `le index`

Walks every repository in the workspace and writes files, directories,
containment edges and lexical chunks. `--json` for statistics.

## `le graph`

| Command | |
|---|---|
| `stats` | Node and edge counts by kind and evidence category |
| `impact <symbol>…` | What a change to these symbols would affect |
| `search <query>` | Retrieve context the way a task step would |

`impact --change` takes one of: `signature`, `behaviour`, `remove`, `rename`,
`add_field`, `schema`, `config`, `route`. The verdict for each consumer comes
from a deterministic table over the change kind and the edge kind.

`search --expand N` sets the graph expansion depth from the lexical anchors;
`0` disables expansion.

## `le plan`

```
le plan <requirement…> [--apply] [--max-steps N]
```

Decomposes a requirement into independent, verifiable steps, each with the
narrowest scope that contains its change. Nothing is created without `--apply`.

The plan is produced by a model but is not trusted by one. A step is rejected
before anything runs when it declares no scope, names a scope escaping the
repository, uses an unknown verification level, or depends on a step that does
not come before it. Those are the failures that would otherwise be discovered
mid-execution, with a half-applied change in a worktree.

## `le gate`

| Command | |
|---|---|
| `list` | Gates waiting for a decision (`--all` includes decided ones) |
| `show <id>` | The gate and the evidence behind it (`--diff` for the full diff) |
| `approve <id> --note "…"` | Approve |
| `reject <id> --note "…"` | Reject |

A gate is a point where a decision leaves the system. Each carries the
deterministic evidence — an impact report, a diff, the verification findings —
so answering means reading what the supervisor computed rather than trusting a
summary.

Gates are journalled *before* they block, so an interrupted approval is a
pending gate on restart rather than a lost one.

Which decisions open a gate is configuration (`gates:` in `le.yaml`). The
shipped default gates breaking changes, out-of-scope writes and applying a
change; it does not gate plans. A budget increase is always a person's call —
the budget exists precisely so a task cannot decide to keep going.

The `--note` matters more than the verdict: it is what the gate is worth six
months from now.

## `le task`

| Command | |
|---|---|
| `verify` | Put the current worktree under the completion contract |
| `create` | Create a task |
| `run <task-id>` | Run a task to a terminal state |
| `list` | Tasks in this workspace |
| `journal <task-id>` | The operation journal; `UNCERTAIN` marks a missing outcome |
| `recover` | Reconcile every non-terminal task and report resumable state |

### `le task verify`

Creates a task, gives it a fresh git worktree, **syncs your uncommitted
changes into it**, runs the verification recipes inside a sandbox, records
every result as evidence tied to the exact content hash it describes, and
reports whether the completion contract is met.

| Flag | |
|---|---|
| `--verify` | `low` (build only), `standard` (build, vet, test), `high` (adds race and format) |
| `--committed` | Verify the last commit instead of your working tree |
| `--json` | Machine-readable outcome |

Exit status is 1 when the contract is not met, so it composes in a script.

The output always says which state it examined. A verdict that does not say
what it looked at is not usable evidence — and verifying the last commit while
you are looking at uncommitted changes would produce a pass describing code
nobody is running.

### `le task create` / `le task run`

`create` flags: `--title` (required), `--verify`, `--requirement`, `--scope`
(repeatable path prefixes the task may change), `--attempts`.

`run` flags: `--json`, `--diff`.

A task runs in its own worktree; your working copy is never touched. Every
action is journalled intent-first, so an interruption at any point leaves a
state `le task recover` can reconcile.

**A change outside `--scope` blocks acceptance** even when every recipe passes.
This is the out-of-scope check of the isolation model: such writes are detected
by diff rather than prevented, because a task must be able to edit its worktree.

### The completion contract

A task is accepted only when:

1. every recipe kind its verification level requires has a result,
2. that result is a **pass** — a skip or an error satisfies nothing,
3. it was produced against the **current** candidate, so evidence for an older
   state cannot be reused,
4. no file changed outside the declared scope.

An engine's claim that it finished is an input to that decision and never the
decision itself.

`recover` reports, per task: the current candidate hash, whether the worktree
drifted, each uncertain operation's classification, how many validations are
stale, whether it is safe to resume, and the next action.

## `le backup` / `le restore`

`backup` writes a consistent online snapshot. `--all` covers every workspace;
`--to` sets the destination.

`restore --from <dir>` restores into the current workspace. The supervisor must
not be running. `--force` overwrites. Restored files are re-opened and verified
before the command returns.

## `le models`

| Command | |
|---|---|
| `bench` | Measure this machine and propose a hardware profile |
| `health` | Probe every declared provider |

`bench` flags: `--write` (save the profile), `--iterations`, `--prompt-tokens`,
`--output-tokens`, `--context`, `--profile-name`, `--json`.

## `le config`

| Command | |
|---|---|
| `init` | Write default `le.yaml` and `providers.yaml` |
| `show` | Effective configuration, providers and role routing |
| `profiles` | Available profiles; `*` is active, and each says shipped or yours |

## `le api`

Runs the supervisor: migrates schemas, validates configuration, selects the
sandbox, starts children, serves `/healthz` and `/readyz`. This is the
container's default command.

`--addr` overrides the listen address. Inside a container bind `0.0.0.0` and
restrict exposure with `-p 127.0.0.1:7777:7777`.

## Hidden commands

`__sandbox-exec` is the Landlock re-exec helper. It is internal, and fails if
invoked without the environment the runner sets.
