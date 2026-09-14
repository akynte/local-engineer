# local-engineer

A supervised local coding engineer: a deterministic harness around a local
model, with strict per-project isolation, an intent-first execution journal,
and evidence-backed verification.

> **Status: pre-1.0, under active development.** The full task pipeline works:
> retrieval, a bounded tool loop that edits a confined worktree, verification
> inside a Landlock sandbox, a completion contract decided from evidence, and
> a human gate carrying the diff before anything is applied.
>
> What is **not** done: language analyzers beyond Go, and the evaluation
> harness. No task-success numbers are published, so this README claims none.
> See [ROADMAP.md](ROADMAP.md).

## What it is

Most local coding assistants lose to two problems: a small context window and
a model that cannot hold a large codebase in its head. This project attacks
both from outside the model.

- **Every question about the repository is answered before the model sees
  anything.** A typed code graph, a lexical index and an execution journal live
  in SQLite. The supervisor answers structural questions from those, and hands
  the model only the slices the current step needs. The context window stops
  being the binding constraint.
- **Every project is isolated by construction.** A workspace is the unit of
  isolation, identified by a hash pinned in the repository. Its index, ledger,
  telemetry, cache, artifacts and engine directories are separate files in
  separate directories, and the storage API cannot be called without a
  workspace handle. There is no global memory and no cross-project channel.
- **Work survives interruption.** Every model-visible action journals its
  intent *before* the side effect and its outcome *after*. A crash leaves
  uncertainty, and recovery resolves it by inspecting the worktree rather than
  assuming success or failure.
- **A model's claim of success decides nothing.** A task is accepted only when
  every check its level requires has a passing result *against the current
  state of the code*. A skip, an error, or a pass against an older state
  satisfies nothing. Then a human gate carries the diff and the findings, so
  approving means reading what the supervisor computed rather than trusting a
  summary.
- **Claims are backed by evidence, not by the model's opinion.** Compatibility
  verdicts come from a deterministic table over the change kind and the edge
  kind. A missing edge means "not discovered", never "does not exist", and the
  report says so every time.

## Install

Docker is the only host dependency. For GPU inference you also need the NVIDIA
driver and the NVIDIA Container Toolkit.

```bash
docker volume create le-data
docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data

docker run -d --name local-engineer \
  --gpus all \
  -v le-data:/data \
  -v "$HOME/code":/work \
  -p 127.0.0.1:7777:7777 \
  ghcr.io/akynte/local-engineer:latest
```

Omit `--gpus all` on a CPU-only host. Publish on `127.0.0.1` — the supervisor
can run sandboxed commands and read every indexed repository, so it must not be
reachable from your network.

Then:

```bash
docker exec -it local-engineer le doctor          # what is actually in effect
docker exec -it local-engineer bash -c 'cd /work/my-project && le workspace init && le index'
docker exec -it local-engineer bash -c 'cd /work/my-project && le graph impact MyFunc --change signature'
docker exec -it local-engineer bash -c 'cd /work/my-project && le task verify'
docker exec -it local-engineer bash -c 'cd /work/my-project && le plan "add retries to the payment client"'
```

`docker compose -f deploy/docker-compose.yml up -d` wraps the same thing.
`deploy/docker-compose.split.yml` is the conventional multi-container layout
for anyone who prefers it.

## What `le doctor` tells you

It is the first command to run and the one to attach to a bug report. It
reports what is *actually* in effect, not what the design hopes for:

```
[ok  ] container boundary (DR-3 layer 1)  /.dockerenv is present
[ok  ] landlock (DR-3 layer 2)            ABI 6; TCP rules enforced
[warn] bubblewrap (DR-3 layer 3)          bwrap probe failed: No permissions to create new namespace
[warn] network containment                Landlock TCP rules do not cover Multipath TCP sockets…
[ok  ] data directory                     /data on ext4
[ok  ] hardware profile                   reference-8gb-cuda-64gb-ram: context 32768, packet cap 12000
[ok  ] index freshness                    last indexed 2m ago; 41231 nodes, 98204 edges
```

## Honest limitations

- **The default container gives strong isolation from your host and between
  workspaces; process-level isolation between concurrent tasks requires the
  optional namespace mode.** Unprivileged user namespaces are usually
  unavailable inside a container, so the bubblewrap layer is off by default.
- **Landlock's TCP rules do not cover Multipath TCP sockets**, and Go's
  `net.Listen` uses MPTCP by default. Port restrictions are therefore
  augmented by the container's network configuration and an allowlisting
  proxy, never relied on alone.
- **SQLite needs a real filesystem with working `fsync`.** Do not put `/data`
  on an overlay layer or a network share. `le doctor` fails if you do.
- **Downgrades across schema versions are not supported.** Migrations are
  forward-only; `le backup` before every upgrade.
- **The shipped hardware profiles are starting points, not measurements.** Run
  `le models bench --write` on your own machine; `le doctor` warns until you do.
- **No task-success numbers exist.** The evaluation harness is built and
  tested and the task set is validated on every CI run, but no run against a
  real model has been done — so nothing here claims a success rate, and the
  code graph's contribution is *measurable*, not *measured*. Storage and graph
  latency numbers **are** published. See
  [the results directory](docs/benchmarks/results/) for what is and is not
  there.
- **TypeScript has no analyzer yet.** Go, SQL schemas, Dockerfiles, Makefiles,
  compose, Kubernetes and Terraform do. The
  [graph schema reference](docs/reference/graph-schema.md) marks the state per
  relationship, because a schema describing edges the code does not emit would
  make impact reports look better than they are.
- **Helm templates are recorded, not parsed.** Rendering a chart needs values
  the analyzer does not have, and a half-rendered template read as YAML
  produces confident nonsense.

## Documentation

Documentation follows the [Diátaxis](https://diataxis.fr) split:

- **Tutorials** — [First task in 15 minutes](docs/tutorials/first-task.md),
  [Index a Go microservice](docs/tutorials/index-a-go-service.md)
- **How-to** — [install](docs/how-to/install.md),
  [configure models](docs/how-to/configure-models.md),
  [choose a hardware profile](docs/how-to/choose-a-profile.md),
  [persistent storage](docs/how-to/persistent-storage.md),
  [back up and restore](docs/how-to/backup-and-restore.md),
  [run fully offline](docs/how-to/run-offline.md),
  [troubleshooting](docs/how-to/troubleshooting.md)
- **Reference** — [CLI](docs/reference/cli.md),
  [configuration](docs/reference/configuration.md),
  [graph schema](docs/reference/graph-schema.md),
  [HTTP API](docs/reference/http-api.md)
- **Explanation** — [architecture](docs/explanation/architecture.md),
  [isolation model](docs/explanation/isolation-model.md),
  [why small models can work here](docs/explanation/why-small-models.md),
  [decision records](docs/adr/)

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md). `make check` reproduces the CI gates
locally. Security issues: [SECURITY.md](SECURITY.md) — please use private
reporting, not a public issue.

## Licence

Apache-2.0. See [LICENSE](LICENSE) and [NOTICE](NOTICE).
