# Start, stop and update

## Start

```console
$ docker start local-engineer
$ curl -fsS http://127.0.0.1:7777/readyz
```

On start the supervisor migrates the data directory's schemas forward,
validates the configuration, probes the isolation layers, starts its children,
and only then reports ready.

`/healthz` answers as soon as the process is alive. `/readyz` answers 200 only
when every essential child is ready, and 503 with reasons otherwise:

```json
{
  "ready": false,
  "reasons": ["llama-server is starting"],
  "children": [{"name": "llama-server", "state": "starting", "restarts": 0}]
}
```

That distinction matters for an orchestrator: `/healthz` says "do not restart
me", `/readyz` says "do not send me work yet".

## Stop

```console
$ docker stop local-engineer
```

`SIGTERM` starts the shutdown sequence:

1. running tasks are paused with a handoff record,
2. engine sessions are aborted and sandboxes terminated,
3. children are stopped in reverse start order, each by signalling its whole
   process group so a test runner cannot leave helpers behind,
4. every SQLite write-ahead log is flushed,
5. the process exits.

The grace period is 30 seconds by default. Docker's own default is 10, so the
shipped compose file sets `stop_grace_period: 40s` — otherwise Docker would
`SIGKILL` the supervisor partway through step 4.

To change it:

```yaml
api:
  shutdown_grace_seconds: 60
```

and raise `stop_grace_period` to match.

## A hard kill is safe

`docker kill`, a power cut and an OOM kill all land in the same place: the
journal has an operation with an intent and no outcome. On the next start,
recovery inspects the worktree and classifies it — applied, not applied, or
partially applied — rather than assuming.

```console
$ le task recover
task t-91f2 — fix the nil dereference in the user loader
  candidate:   3f9a1c22e8b0
  drifted:     false
  uncertain:   1 operation(s)
    seq 14 edit → complete (file matches the intent's after-hash)
  validations: 3, 1 stale
  safe:        true
  next:        re-run stale validations against the current candidate
```

See [crash recovery](../explanation/crash-recovery.md).

## Update

**Back up first.** Migrations are forward-only.

```console
$ docker exec local-engineer le backup --all
$ docker pull ghcr.io/akynte/local-engineer:latest
$ docker rm -f local-engineer
$ docker run -d --name local-engineer … ghcr.io/akynte/local-engineer:latest
$ curl -fsS http://127.0.0.1:7777/readyz
```

With compose:

```console
$ docker compose -f deploy/docker-compose.yml pull
$ docker compose -f deploy/docker-compose.yml up -d
```

### Downgrades

Not supported across a schema version. A database from a newer build is refused
with an explanation rather than misread:

```
le: store: /data/workspaces/…/index.db is at schema 2, this build understands 1;
downgrades across schema versions are not supported, restore a backup or upgrade
```

## Logs

```console
$ docker logs -f local-engineer
$ docker exec local-engineer le doctor
```

For machine-readable logs:

```console
$ docker run -d -e LE_LOG_JSON=1 … 
```

or pass `--log-json`. `-v` raises the level to debug; `-q` drops it to errors.
