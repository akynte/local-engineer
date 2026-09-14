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

## `le task`

| Command | |
|---|---|
| `list` | Tasks in this workspace |
| `journal <task-id>` | The operation journal; `UNCERTAIN` marks a missing outcome |
| `recover` | Reconcile every non-terminal task and report resumable state |

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
