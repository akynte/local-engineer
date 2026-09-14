# Install

## Prerequisites

The only host dependency is a container runtime:

- **Docker Engine 24+**, or Podman with the compat socket.
- For GPU inference: the **NVIDIA driver** and the **NVIDIA Container
  Toolkit** (or the ROCm equivalents).

Nothing else is installed on your host. No Go, no Node, no Python.

## Choose an image

| Image | Platforms | Contents |
|---|---|---|
| `ghcr.io/akynte/local-engineer:latest` | amd64 | CUDA build with an embedded inference server, plus toolchains |
| `…:latest-cpu` | amd64, arm64 | Toolchains and analysis tools, no embedded inference |
| `…:latest-slim` | amd64, arm64 | Supervisor and engine only; bring your own toolchains and inference |

On Apple Silicon and CPU-only hosts, use `-cpu` and run inference externally
(see [configure models](configure-models.md)).

## Single container (the default)

```console
$ docker volume create le-data
$ docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data
$ docker run -d --name local-engineer \
    --gpus all \
    -v le-data:/data \
    -v "$HOME/code":/work \
    -p 127.0.0.1:7777:7777 \
    ghcr.io/akynte/local-engineer:latest
```

A named volume is created owned by root and the container runs as uid 10001, so
the one-time `chown` is required. Omit `--gpus all` on a CPU-only host.

**Publish on `127.0.0.1`.** `-p 7777:7777` would expose a supervisor that can
run sandboxed commands and read every repository you index to your whole
network. The supervisor warns at startup if you do it anyway.

## Compose

```console
$ LE_WORK="$HOME/code" docker compose -f deploy/docker-compose.yml up -d
$ docker compose -f deploy/docker-compose.yml exec local-engineer le doctor
```

`deploy/docker-compose.split.yml` is the conventional multi-container layout,
with inference in its own container. It is maintained and validated in CI.

## Verify the install

```console
$ docker exec local-engineer le version
$ docker exec local-engineer le doctor
```

`le doctor` exits 0 when everything is clean, 1 on warnings, 2 on failures.
A warning about bubblewrap is expected inside a container — see
[the isolation model](../explanation/isolation-model.md).

## On your host, without a container

Supported for development, not the documented default:

```console
$ go install github.com/akynte/local-engineer/cmd/le@latest
$ export LE_DATA="$HOME/.local/share/local-engineer"
$ le config init
```

The host-isolation guarantees do not apply: layer 1 of the sandbox is the
container, and there is no container. `le doctor` says so.

## What `le` looks like when it is working

<!-- test:run -->
```console
$ le version
local-engineer …
schemas: index=… ledger=… telemetry=…
```

The schema numbers move with each release. `le` refuses to open a data
directory written by a newer build rather than misreading it, so a mismatch is
a clear message, not corruption.

## Uninstall

```console
$ docker rm -f local-engineer
$ docker volume rm le-data
```

Removing the volume deletes every workspace's index, ledger, artifacts and
telemetry. The `.le/workspace.yaml` files in your repositories are untouched,
so re-indexing later reuses the same workspace ids.
