# Run fully offline

No network egress at all: no model downloads, no package fetches, no telemetry,
no remote provider.

## 1. Prepare before going offline

Model weights, dependencies and toolchains must already be present:

```console
$ docker pull ghcr.io/akynte/local-engineer:latest
$ docker run --rm -v le-models:/models alpine sh -c 'ls /models'
```

Mount your model directory in addition to the data volume:

```console
$ docker run -d --name local-engineer \
    --gpus all \
    -v le-data:/data \
    -v /srv/models:/data/models:ro \
    -v "$HOME/code":/work \
    -p 127.0.0.1:7777:7777 \
    ghcr.io/akynte/local-engineer:latest
```

## 2. Turn offline mode on

In `/data/config/le.yaml`:

```yaml
offline: true
inference:
  mode: embedded
```

Or with an environment variable:

```console
$ docker run -d -e LE_OFFLINE=1 … ghcr.io/akynte/local-engineer:latest
```

With `offline: true`, a provider that is not local is refused when the router
is built — at startup, before any request can be made. A misconfiguration
becomes a failure to start rather than a silent egress.

`offline: true` also refuses to coexist with `egress.enabled: true`. The §6.1
provisioning lanes exist to reach an allowlisted host, and offline mode has no
route out; rather than one setting quietly overriding the other, the pair is a
configuration error. If you need to fetch something, do it before going
offline — see [fetch a dependency](fetch-dependencies.md).

An external provider URL that is not loopback is refused too:

```
le: configuration is invalid, refusing to start: config: offline is set but
inference.base_url "https://api.example.com" is not local
```

## 3. Remove the network entirely

Offline mode is a policy inside the process. For a hard guarantee, take the
network away:

```console
$ docker run -d --name local-engineer \
    --network none \
    -v le-data:/data \
    -v /srv/models:/data/models:ro \
    -v "$HOME/code":/work \
    ghcr.io/akynte/local-engineer:latest
```

With `--network none` you lose the published port, so use `docker exec` for the
CLI:

```console
$ docker exec -it local-engineer le doctor
```

Inference must be embedded, because there is no network to reach an external
server over.

## 4. Verify

```console
$ docker exec local-engineer le config show | jq '.config.offline, .providers'
true
["local"]
```

```console
$ docker exec local-engineer le doctor
```

## What still works offline

Everything deterministic: indexing, the graph, impact analysis, retrieval, the
journal, recovery, backup and restore. All of it is local computation over
local files.

## What does not

- Dependency resolution that needs a registry. Vendor your dependencies, or
  prime the module and package caches before going offline.
- Any remote provider. That is the point.
- Documentation lookups that would fetch a page.

## A note on what "offline" protects

Offline mode stops *this software* from sending your code anywhere. It is a
guarantee about egress, not about the sandbox: a task still runs commands from
your repository, and those commands are bounded by the container and the
Landlock rules, not by this setting. See
[the isolation model](../explanation/isolation-model.md).
