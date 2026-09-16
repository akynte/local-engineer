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
| `retry <task-id>` | Return a failed task to pending, keeping its id, journal and worktree |

### `le task retry`

A failed task is not always work that could not be done. An output budget too
small for the model's reasoning, a request longer than the provider's timeout,
or a machine under memory pressure all produce a failed task whose work was
never really attempted — and fixing the cause does not help on its own, because
`le task run` refuses a task in a terminal state.

Retry returns it to `pending`. The id, the journal and the worktree are kept, so
the record of what was already tried survives; creating a new task with the same
description loses it.

An accepted task is refused: its change has been through the completion contract
and may already be merged, so running it again would redo approved work against
evidence that no longer describes the worktree.

| Flag | |
|---|---|
| `--reason` | What you changed so this run goes differently. Recorded in the journal as a decision |

### `le task verify`

Creates a task, gives it a fresh git worktree, **syncs your uncommitted
changes into it**, runs the verification recipes inside a sandbox, records
every result as evidence tied to the exact content hash it describes, and
reports whether the completion contract is met.

| Flag | |
|---|---|
| `--verify` | `low` (build only), `standard` (build, vet, test, format), `high` (adds race and — where the repository declared them — lint, semgrep, generator checks and integration steps) |
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
4. **no check that ran found a problem**, including the conditional kinds
   (`lint`, `analyzer`) the level does not demand a result from,
5. no file changed outside the declared scope.

Rules 1 and 4 answer different questions. A level says which kinds must have
produced evidence; `lint` and `analyzer` cannot be on that list because they
run only where the repository committed a configuration, and demanding them
unconditionally would fail every repository that committed neither. But a check
that *did* run and *did* find something is evidence about this code, so it
disqualifies regardless of level. An `error` still does not: a tool that could
not run says nothing about the code.

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
| `conformance` | Check a provider against what `providers.yaml` declares about it |
| `needle` | Measure the packet size this model can actually retrieve from |

`bench` flags: `--write` (save the profile), `--iterations`, `--prompt-tokens`,
`--output-tokens`, `--context`, `--profile-name`, `--json`.

`conformance` flags: `--provider`, `--json`. DR-4 makes a provider's
`Capabilities()` something callers rely on rather than a hint, so the
declarations in `providers.yaml` are worth verifying rather than trusting. A
capability a provider accepts but does not declare is reported as `unproven`,
not as a failure: the contract is that a declaration must hold, not that
everything working must be declared.

`needle` flags: `--sizes` (packet sizes to try, in tokens), `--write` (save the
measured cap into the active profile), `--json`.

`needle` is what §8.3 means by "the needle test sets the hard packet cap per
model profile". A window a model *accepts* and a window it *retrieves from* are
different sizes, and `max_packet_tokens` derived as half the context window is a
statement about arithmetic rather than about the model. The command hides a
random access code at several depths in a packet of Go-shaped filler and asks
for it back.

Two things about how it reports:

- **The cap is the largest size where every depth is recalled**, not where the
  average is good. A packet builder cannot choose where in a packet the needed
  slice lands, so a size that works at the edges and fails in the middle is a
  size that fails.
- **Every size is the provider's own token count**, not the size asked for. The
  sweep calibrates against the provider first — two probe-shaped requests solved
  for characters-per-token and fixed prompt overhead — because a cap derived
  from an estimate is an estimate wearing a measurement's clothes. Against a
  provider that reports no usage the numbers are the sizes requested, the report
  says so in its header, and `--write` refuses.

A provider refusing a request larger than its window is not a recall failure:
the model was never asked. That is reported as running out of context rather
than out of recall, and the cap it yields is a floor rather than a ceiling.

## `le eval`

| Command | |
|---|---|
| `run` | Run the task set and report the results |
| `tasks` | List and validate the task set |
| `report` | Render a saved result file |
| `arms` | Describe the configurations being compared and what each isolates |

`run` flags: `--arms`, `--tasks` (the task set directory), `--task` (run only
these ids), `--repeat`, `--out`, `--json`.

Two properties are what make the numbers mean anything. A task's acceptance
tests are never in the worktree while the task runs, so a model cannot satisfy a
test it can read. And the system's own verdict is recorded separately from the
ground truth, so "claimed success and was wrong" is its own number rather than
something averaged away — read a solved rate without the false-acceptance rate
beside it and you are reading half the result.

`--repeat` **defaults to 3** because one run of a cell is a sample rather than
a measurement. The first real run of this harness changed verdict on 4 of 12
task/arm cells between passes, which is why repetition is the default rather
than something to remember. `--repeat 1` is still accepted, and the report says
plainly that a single pass measures nothing. See
[the published results](../benchmarks/results/2026-09-14-tasks.md) and
[the methodology](../benchmarks/METHODOLOGY.md).

## `le memory`

| Command | |
|---|---|
| `add` | Record a note |
| `list` | Show the notes kept with this repository |
| `remove` | Delete a note |

`add` flags: `--kind` (required), `--source`, `--evidence`, `--tag`.
`list` flags: `--kind`, `--json`. `remove` flags: `--kind` (required).

Notes come in three kinds because they are not interchangeable:

| Kind | |
|---|---|
| `intent` | Why something is being done: a requirement, a constraint |
| `observation` | Something seen once — evidence, not a rule |
| `advice` | A rule meant to steer future work |

Every note records where it came from, and `--source` cannot be dropped. A rule
with no source cannot be judged, and a system that writes rules about its own
work will write flattering ones. Notes live under `.le/memory/` so they travel
with the repository; there is no global store.

## `le lessons`

| Command | |
|---|---|
| `export` | Write this repository's notes to a file |
| `import` | Copy notes from a file into this repository, after showing them |

`export` flags: `--kind` (defaults to `advice`), `--to`. `import` flags:
`--kind`, `--yes`.

There is no global memory and no automatic channel between projects. These two
commands are the channel: `export` writes a file you can read, and `import`
shows you what it would copy before copying it. Imported notes are marked as
imported and keep their original source, so a rule learned elsewhere never reads
as one this repository established — the difference between a lesson and a
rumour.

`export` defaults to `advice` alone: `intent` is usually specific to the
repository that recorded it, and `observation` is evidence about one codebase
rather than a rule about any.

## `le deps`

The dependency provisioning lane of §6.1: a confined process whose only route
out is the allowlisting egress proxy.

| Command | |
|---|---|
| `sync` | `go mod download all` inside the deps lane |
| `run -- <cmd>` | Any command inside the deps lane |

**These are not part of a task, and that is the design.** A task sandbox has no
egress at all: verification runs with `GOPROXY=off`, and a task's Landlock
ruleset grants the inference endpoint and assigned test ports and nothing else.
There is no per-task flag that changes this — the only function that builds a
spec containing the proxy port takes a lane, and a task runner cannot construct
one.

So a change that needs a new dependency is an operator running `le deps sync`
between tasks, with the `go.mod` diff visible before any task verifies against
it.

Both lanes require `egress.enabled` in `le.yaml`, which is **off by default**,
and both refuse to run when `offline` is set. Each lane has its own listener and
its own slice of the allowlist, so a documentation host cannot be used to fetch
code.

A refused host is reported by the supervisor as `egress refused` with the host,
the lane and the reason. The fix is an entry in `egress.allowlist` with a `why`
— the reason is required so the decision is legible later. `le doctor` reports
the proxy's state and warns about wildcard rules.

## `le docs`

The documentation provisioning lane, separate from `le deps` so that a
documentation host cannot be used to fetch code.

| Command | |
|---|---|
| `fetch <url>` | Fetch one URL through the docs lane; `--output` writes a file |
| `run -- <cmd>` | Any command inside the docs lane |

The same rules apply as for `le deps`: `egress.enabled` must be on, `offline`
refuses, the lane is confined, and only hosts the allowlist names for the
`docs` lane are reachable.

## `le tui`

A repainting status view for use inside the container (§4.1):

```console
$ docker exec -it local-engineer le tui
```

It shows what `le doctor` cannot — what is happening *now*: the supervisor's
children and their restart counts, the active sandbox layers, non-terminal
tasks, and any gate waiting for a decision.

| Flag | |
|---|---|
| `--interval` | refresh period (default 2s) |
| `--once` | render a single frame and exit |

Deliberately not a full-screen application. Without a TTY it appends plain
blocks instead of repainting, so it can be piped or redirected to a log — and a
cursor-addressed UI would add a dependency and break under `docker exec`
without a terminal.

**It is read-only, and that is not an omission.** Approving a gate is
`le gate approve`, with the diff and the impact report in front of you. A key
that approved from a status screen would be a way to approve without reading,
which is the failure the gates exist to prevent.

## `le telemetry`

| Command | |
|---|---|
| `show` | counters for the workspace containing the working directory |
| `aggregate build` | collect counters from every workspace |
| `aggregate show` | report counters across workspaces |
| `aggregate forget <id>` | remove one workspace from the aggregate |

Telemetry is per workspace, like everything else (§2.2). The aggregate is the
**one** exception §2.2 permits — "an optional aggregate with workspace ids
only", holding "counters, never content" — and it is bounded accordingly:

- It stores a workspace id, a metric name, a UTC day and two numbers. There is
  nowhere in the row shape to put a path, a symbol or a task title, which is how
  "never content" is enforced rather than promised. A test asserts the column
  set, because adding a column is exactly how that would stop being true.
- A **day** is the finest resolution. Per-second counters across workspaces
  would be a timing channel between projects, which §2.2's slot clearing is
  careful to avoid elsewhere.
- It is built by `build`, never written continuously, so nothing accumulates
  across your projects while you are not looking. `show` says out loud that its
  numbers are as of the last build.
- It is derived and disposable: deleting the file loses nothing that is not
  still in each workspace's own telemetry.

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

`verify-declared --kind <generate|integration> --name <step>` runs one step
from `.le/verify.yaml` and prints its result as JSON. It is hidden because it
is not an interface: it is how a recipe expresses a *sequence*. A recipe is
argv and never a shell string — a shell inside the sandbox would make the
argument boundary meaningless — and neither declared kind is one command. An
integration step is up-then-test-then-down with the teardown guaranteed; a
generate check is snapshot-run-compare-restore. See
[declare runtime and generation checks](../how-to/declare-runtime-checks.md).
