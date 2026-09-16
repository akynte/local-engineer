# local-engineer

[![CI](https://github.com/akynte/local-engineer/actions/workflows/ci.yml/badge.svg)](https://github.com/akynte/local-engineer/actions/workflows/ci.yml)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/akynte/local-engineer/badge)](https://scorecard.dev/viewer/?uri=github.com/akynte/local-engineer)
[![CodeQL](https://github.com/akynte/local-engineer/actions/workflows/codeql.yml/badge.svg)](https://github.com/akynte/local-engineer/actions/workflows/codeql.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/akynte/local-engineer.svg)](https://pkg.go.dev/github.com/akynte/local-engineer)
[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)

A supervised local coding engineer: a deterministic harness around a local
model, with strict per-project isolation, an intent-first execution journal,
and evidence-backed verification.

## 60 seconds

```console
$ le workspace init          # pin this repository's identity
$ le index                   # build the source index, symbol index and graph
$ le graph impact Total --change signature
3 consumers: 1 breaking, 0 undetermined, 2 behaviour-only, 0 compatible

  …hop/internal/orders.LineTotal breaking     resolved   via calls        depth 1
                                   → update the call site to the new signature

A missing edge means 'not discovered', not 'does not exist'.

$ le task verify             # put the working tree under the completion contract
ACCEPTED  verify-… (1 attempt(s), candidate 761de4ef8354)

RECIPE    KIND   STATUS  SUMMARY
go build  build  pass    compiles
go vet    vet    pass    no vet findings
go test   test   pass    no test packages ran
```

[`docs/demo.cast`](docs/demo.cast) is an asciicast v2 recording of exactly
that. Play it with `asciinema play docs/demo.cast`. It is **generated** by
[`scripts/record-demo.sh`](scripts/record-demo.sh) from real command output
inside the image — never hand-written. A hand-written demo
is a screenshot of a system that may no longer exist, and the whole argument
here is that claims are checkable. Regenerate it after any change that alters
what these commands print.

> **Status: pre-1.0, under active development.** The full task pipeline works:
> retrieval, a bounded tool loop that edits a confined worktree, verification
> inside a Landlock sandbox, a completion contract decided from evidence, and
> a human gate carrying the diff before anything is applied.
>
> What is **not** done: a task set large enough to evaluate against. The
> harness has been run (60 runs, local 35B MoE, results published) and the
> run's own conclusion is that the task set cannot answer the questions it was
> built to ask, so this README claims no success rate. Language coverage is
> uneven rather than absent — Go is deepest, TypeScript needs the sidecar for a
> call graph, and the limitations below say which is which.
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
  `net.Listen` uses MPTCP by default. Port restrictions are therefore augmented
  by the container's network configuration, never relied on alone.
- **A task never reaches the network.** Verification runs with `GOPROXY=off`,
  and a task's Landlock ruleset grants the inference endpoint and assigned test
  ports and nothing else. When a change genuinely needs a new dependency, that
  is an operator's `le deps sync` through the §6.1 allowlisting proxy — a
  separate confined lane, on a port no task is granted, reaching only hosts
  named in `egress.allowlist` with a reason. It is **off by default**; a
  machine whose premise is that it has no egress should not acquire some from
  a shipped config file.
- **SQLite needs a real filesystem with working `fsync`.** Do not put `/data`
  on an overlay layer or a network share. `le doctor` fails if you do.
- **Downgrades across schema versions are not supported.** Migrations are
  forward-only; `le backup` before every upgrade.
- **The shipped hardware profiles are starting points, not measurements.** Run
  `le models bench --write` on your own machine; `le doctor` warns until you do.
- **The published task-success numbers settle nothing.** A real run exists —
  3 tasks × 4 arms × 5 passes, 60 runs against a local model on disclosed
  hardware — but every arm's confidence interval overlaps every other's, 4 of
  the 12 task/arm cells changed verdict between passes, and two of the three
  tasks are solved by every arm on every pass including the bare baseline. So
  nothing here claims a success rate, and the code graph's contribution remains
  *measurable* rather than *measured*: on this set it added zero points and
  roughly doubled the tokens spent on the only task that discriminated. What
  the run does show is that false acceptance is real and quantifiable — the
  unsupervised baseline claimed success on work that failed the hidden test in
  4 of 15 runs. Storage and graph latency numbers **are** published. See
  [the results directory](docs/benchmarks/results/) for what is and is not
  there.
- **TypeScript is type-checked only when the sidecar is installed.** Go, SQL
  schemas, Dockerfiles, Makefiles, compose, Kubernetes and Terraform need
  nothing extra. For TypeScript the `cpu` and `cuda` images carry a Node
  sidecar built on the TypeScript compiler, and its edges — including the call
  graph — are `resolved`. Without it (the `-slim` image, or a host install with
  no Node) the analyzer reads the source instead: imports, declarations,
  heritage clauses and `process.env` reads are indexed, and there is **no call
  graph at all**, because without the compiler an identifier in call position
  may be a local or a shadowed binding, and an edge that is wrong half the time
  is worse than no edge. Every TypeScript edge carries the evidence category
  that produced it, so the two readings are told apart rather than blurred. The
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
