# Install on the host, without Docker

Design v3 §13 keeps host-mode installation supported "for developers but not
the documented default". This page is that path, and the first thing it owes
you is why it is not the default.

## What you give up

**DR-3 layer 1 — the container boundary — is simply absent.**

That layer is what bounds the entire system to the repositories you mounted and
the data volume you gave it. Not just the supervisor: the tests it runs, the
builds, every tool a task invokes. On the host, a task's `go test ./...` runs
against your real filesystem, and the only thing confining it is Landlock.

The [§6.2 guarantee table](../explanation/isolation-model.md#the-guarantee-table)
has a "container only" column for a reason. On a host install, that column is
not available, and the guarantees that rest on it do not hold:

| Guarantee | In a container | On the host |
|---|---|---|
| Cannot touch host files outside mounts | yes | **no such thing as "outside mounts"** |
| Cannot read another workspace's data | yes | Landlock per task only |
| Cannot reach model-management endpoints | yes | Landlock TCP rules only |

`le doctor` reports this every run, permanently, when it cannot find a
container marker. That warning is correct and is not something to silence.

## What you keep

Landlock still confines each task (layer 2), and bubblewrap (layer 3) is
available if your kernel permits unprivileged user namespaces — which it more
often does on a host than inside a container, so a host install is sometimes the
*only* way to get layer 3. The journal, the completion contract, workspace
isolation at the storage layer and every verification recipe work identically.

## Install

```console
$ git clone https://github.com/akynte/local-engineer
$ cd local-engineer
$ scripts/install-bare-metal.sh --check
```

`--check` reports what is present and installs nothing. When you are satisfied:

```console
$ scripts/install-bare-metal.sh
```

It builds `le` with the same `-trimpath` and version stamping the image build
uses, installs it to `~/.local/bin`, and installs the TypeScript sidecar to
`~/.local/share/local-engineer` if Node is available — running the same probe
the image build runs, because a sidecar that is present but broken is worse
than an absent one.

`--prefix DIR` installs elsewhere. `--no-sidecar` skips the Node part.

## After installing

```console
$ export PATH="$HOME/.local/bin:$PATH"
$ export LE_DATA="$HOME/.local/share/local-engineer"
$ export LE_TYPESCRIPT_SIDECAR_DIR="$HOME/.local/share/local-engineer/sidecars/typescript"
$ le doctor
```

`LE_DATA` matters: the default is `/data`, which is the container's volume
path and is not writable on a host.

Then the same first task as everywhere else:

```console
$ cd /path/to/your/repo
$ le workspace init
$ le index
$ le task verify
```

## What will be missing

The script reports these as warnings rather than failing, because each one
degrades a specific thing rather than breaking the install:

| Absent | Effect |
|---|---|
| `node` | TypeScript is read lexically, with **no call graph at all** |
| `ripgrep` | Lexical search falls back to a slower path |
| `golangci-lint` | The `lint` recipe skips — which is not the same as passing |
| `semgrep` | The `analyzer` recipe skips |
| `bubblewrap` | DR-3 layer 3 unavailable |

A skipped recipe satisfies nothing in the completion contract. `le task verify
--verify high` on a machine missing `golangci-lint` is a weaker check than the
same command in the image, and the result says so.

## See also

- [Install with Docker](install.md) — the documented default, one command.
- [The isolation model](../explanation/isolation-model.md) — what each layer
  actually guarantees.
- [Troubleshooting](troubleshooting.md).
