# HTTP API

Served by `le api` on `api.addr`, by default `127.0.0.1:7777`.

**There is no authentication.** The API is protected by binding to loopback and
by the container boundary. It can run sandboxed commands and read every
repository you have indexed, so publish it as `-p 127.0.0.1:7777:7777` and
never as `-p 7777:7777`. The supervisor warns at startup if it is bound to a
non-loopback address outside a container.

## `GET /healthz`

Liveness. Answers 200 whenever the process is running.

```json
{
  "status": "ok",
  "version": {
    "version": "0.1.0", "commit": "abc1234", "go": "go1.26.3",
    "platform": "linux/amd64",
    "schemas": {"index": 1, "ledger": 1, "telemetry": 1},
    "indexer_version": 1, "workspace_id_scheme": 1
  },
  "uptime": "4h12m",
  "pid": 7
}
```

Use this for a liveness probe: a non-200 means restart me.

## `GET /readyz`

Readiness. 200 only when every essential child is ready; 503 with reasons
otherwise.

```json
{
  "ready": false,
  "reasons": ["llama-server is starting"],
  "children": [
    {"name": "llama-server", "state": "starting", "pid": 42,
     "restarts": 0, "last_change": "2026-09-14T10:15:00Z", "essential": true}
  ],
  "profile": "reference-8gb-cuda-64gb-ram",
  "sandbox_layers": ["container", "landlock"]
}
```

Child states: `stopped`, `starting`, `ready`, `degraded` (running but failing
its health check), `failed` (restart budget exhausted), `stopping`.

Use this for a readiness probe and as the container health check. It is how a
single-container deployment regains the per-process visibility an orchestrator
would otherwise have.

## `GET /version`

The version block from `/healthz`, on its own.

## `GET /v1/status`

Everything an operator asks for first: version, uptime, active profile, every
child, the sandbox report, the data directory, and the goroutine count.

## `GET /v1/sandbox`

Which isolation layers are active, which are not and exactly why, the full
guarantee table, and the honest summary statement.

```json
{
  "runner": "landlock",
  "active_layers": ["container", "landlock"],
  "inactive_layers": [
    {"layer": "bwrap", "reason": "bwrap probe failed: No permissions to create new namespace…"}
  ],
  "guarantees": [
    {"statement": "Cannot see other tasks' processes",
     "container_only": false, "with_landlock": false, "with_bwrap": true,
     "note": "requires the PID namespace, which only the bubblewrap layer provides"}
  ],
  "statement": "The default container gives strong isolation from your host and between workspaces; process-level isolation between concurrent tasks requires the optional namespace mode."
}
```

The guarantee table is generated from the same source the documentation quotes,
so the claim and the code cannot drift apart.

## `GET /v1/workspaces`

Every workspace known to this data directory.

```json
[{"id": "uz4wojcvfbigq375a726jvpjh4", "name": "demo",
  "root": "/work/demo", "last_opened": "2026-09-14T10:15:00Z", "scheme_version": 1}]
```

This reads only each workspace's record file — it opens no database, so it
cannot block against a running task.

## `GET /`

The operator dashboard: readiness, supervised children, active isolation
layers, and workspaces. A single self-contained page with no external
resources, because the offline lane forbids egress and a page that fetched a
CDN would be blank for exactly the users who need it.

## Conventions

- All responses are `application/json; charset=utf-8` with
  `X-Content-Type-Options: nosniff`.
- Timestamps are RFC 3339 UTC.
- Errors are `{"error": "…"}` with a meaningful status code.
- Every endpoint is currently read-only. State-changing endpoints will be added
  with the task-execution loop, and will be documented here before they ship.
