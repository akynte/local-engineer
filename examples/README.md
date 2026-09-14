# Examples

Worked repository layouts for trying the system against something realistic.

| Example | What it exercises |
|---|---|
| `go-microservice/` | A conventional Go service: handlers, services, repositories, migrations, config |
| `nuxt-app/` | A TypeScript/Vue frontend with an API layer |
| `monorepo/` | Several services in one workspace, with a shared library |

Each is a minimal but plausible layout — enough structure for the index and the
graph to have something to say, small enough to index in under a second.

## Using one

```console
$ cd examples/go-microservice
$ le workspace init --name example-go
$ le index
$ le graph stats
$ le graph search "user service"
```

## What they are not

They are not a benchmark task set. The evaluation harness and its task set are
Phase 5; see [the benchmark methodology](../docs/benchmarks/METHODOLOGY.md) for
what will be measured and how.

They are also not templates to start a project from. They exist to give the
indexer realistic shapes — nested packages, a layered architecture, a
migration, a compose file — not to demonstrate good service design.
