# Persistent storage

## Where everything lives

One volume, mounted at `/data`:

```
/data/
  config/            le.yaml, providers.yaml, profiles/
  models/            GGUF files, or a bind mount to an existing model directory
  workspaces/<id>/
    index.db         files, nodes, edges, chunks, FTS, embeddings
    ledger.db        requirements, tasks, operations, checkpoints, evidence, leases
    telemetry.db     events, GPU samples
    artifacts/       content-addressed test output, diffs, screenshots
    cache/           package loads, analysis results
    opencode/        the engine's XDG directories
    slots/           saved prompt-cache slots
    tmp/             wiped at task end
  backups/
```

Three database files per workspace rather than one, so a large index rebuild
never blocks the ledger and the ledger can be backed up independently at high
frequency.

Everything a workspace knows is under its own id. There is no shared index, no
shared cache and no global memory.

## What must not host `/data`

**SQLite requires a real filesystem with working `fsync`.** Two things break
it:

- **A container overlay layer** — that is, not mounting a volume at all. The
  data vanishes when the container is removed, and `fsync` does not mean what
  SQLite needs it to mean.
- **A network share** (NFS, SMB, sshfs). SQLite's locking is unreliable there.

Both are checked:

```console
$ le doctor
[ok  ] data directory                     /data on ext4
```

A failure here is exit code 2, and the entrypoint warns before the supervisor
even starts.

## Named volume or bind mount

A named volume is the default and the simplest:

```console
$ docker volume create le-data
$ docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data
```

A bind mount works too, and makes the data easier to inspect from the host:

```console
$ mkdir -p ~/.local/share/local-engineer
$ sudo chown -R 10001:10001 ~/.local/share/local-engineer
$ docker run -d -v ~/.local/share/local-engineer:/data … 
```

If you prefer not to chown, run the container as yourself with
`--user "$(id -u):$(id -g)"`. The container's own uid is 10001 only so that it
is not root.

## Your repositories

Mount them at `/work`:

```console
$ docker run -d -v "$HOME/code":/work …
```

This is read-write, because tasks edit code. The container can reach nothing
else on your host: that is the first isolation layer, and it is the one that is
always in effect.

To mount a single project read-only for indexing without edits:

```console
$ docker run -d -v "$HOME/code/myproject":/work/myproject:ro …
```

## Size

The index is roughly proportional to source size. As a rough guide, a
100k-line Go repository produces an `index.db` in the low tens of megabytes.
Artifacts grow with task history and are content-addressed, so repeated
identical output costs nothing.

```console
$ le doctor --json | jq '.checks[] | select(.name=="index freshness")'
```

## Moving to another machine

Back up, copy, restore. See [back up and restore](backup-and-restore.md). The
workspace ids are pinned in each repository's `.le/workspace.yaml`, so as long
as the repositories come too, the restored data reattaches to the right code.
