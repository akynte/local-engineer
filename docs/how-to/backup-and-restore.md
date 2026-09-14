# Back up and restore

**Back up before every upgrade.** Migrations are forward-only and downgrades
across a schema version are not supported; a backup is the only way back.

## Back up

```console
$ docker exec local-engineer le backup --all
backed up … to /data/backups/…
```

This uses SQLite's online snapshot path, so it is safe while the supervisor is
working. Each workspace's three databases are written side by side under a
timestamped directory.

For one workspace, run it from inside that repository:

```console
$ le backup
```

To a specific location:

```console
$ le backup --to /data/backups/before-upgrade
```

To get the backup off the volume entirely:

```console
$ docker cp local-engineer:/data/backups ./le-backups
```

Or back up the whole volume:

```console
$ docker run --rm -v le-data:/data -v "$PWD":/out alpine \
    tar czf /out/le-data.tar.gz -C /data .
```

## Restore

The supervisor must not be running against the data directory.

```console
$ docker stop local-engineer
$ docker run --rm -it -v le-data:/data -v "$HOME/code":/work \
    ghcr.io/akynte/local-engineer:latest \
    bash -c 'cd /work/myproject && le restore --from /data/backups/20260914T101500Z/<workspace-id>'
restored index.db
restored ledger.db
restored telemetry.db
workspace … restored and verified
```

Restore removes the write-ahead log alongside each database. A restored
database paired with the previous WAL is silent corruption, so this is not
optional.

The restored files are re-opened and verified before the command returns. A
database carries the id of the workspace that created it, so restoring into the
wrong workspace fails here rather than serving you another project's code:

```
le: store: /data/workspaces/…/index.db belongs to workspace … but was opened as …
```

## Verify a backup

```console
$ le doctor --deep
[ok  ] integrity: index      integrity_check ok
[ok  ] integrity: ledger     integrity_check ok
[ok  ] integrity: telemetry  integrity_check ok
```

`--deep` runs a full `PRAGMA integrity_check`, which is slow on a large index.
The startup check is a `quick_check` on every open.

## What a backup does not contain

- Your repositories. They are yours, and git already has them.
- Model weights. Re-download them; `/data/models` can be a separate mount.
- The `.le/workspace.yaml` files. Those live in the repositories and should be
  committed.

A restored `/data` plus your repositories is a complete recovery.

## Upgrade sequence

```console
$ le backup --all
$ docker pull ghcr.io/akynte/local-engineer:latest
$ docker rm -f local-engineer
$ docker run -d --name local-engineer … ghcr.io/akynte/local-engineer:latest
```

On start, `le` migrates the schemas forward and reports readiness. If a
migration fails, the container will not become ready and the backup is your
route back.
