# Index a Go microservice

A worked example on a realistic layout, showing what the graph can and cannot
tell you today.

## The repository

A conventional Go service:

```
payments/
  go.mod
  cmd/api/main.go
  internal/
    handler/payment.go      HTTP handlers
    service/payment.go      business logic
    repository/payment.go   database access
    config/config.go        environment configuration
  migrations/001_payments.sql
  deploy/docker-compose.yml
```

## Create the workspace and index

```console
$ cd payments
$ le workspace init --name payments
$ le index
payments: 12 files, 34 chunks, 28 nodes, 27 edges (0 skipped) in 18ms
```

Commit `.le/workspace.yaml`. It pins the workspace identity to the repository,
so moving or re-cloning the directory keeps the same index, ledger and history.

## A multi-repository workspace

If the service is one of several in a monorepo, or you work across several
repositories as one system, declare them together:

```console
$ le workspace init --name platform --repo services/payments --repo services/users --repo libs/shared
```

One workspace, several repositories, each with its own repository id. They
share an index — because they are one system — and remain isolated from every
*other* workspace.

## Look at what was indexed

```console
$ le graph stats
28 nodes, 27 edges

by relationship:
  contains       27

by evidence category:
  resolved       27
```

**Read that honestly.** Today the graph holds the filesystem and containment
layer: directories, files, and what contains what, all sourced from the
filesystem and therefore `resolved`.

It does not yet hold `calls`, `implements`, `reads_config` or `routes_to`
edges, because the Go language analyzer is not implemented. The
[graph schema reference](../reference/graph-schema.md) marks exactly which
relationships are implemented and which are declared but not yet produced. A
schema that described edges the code does not produce would make the impact
reports look better than they are.

## Retrieval works now

Lexical anchors and containment expansion are enough to be useful:

```console
$ le graph search "payment repository Load" --expand 2
6 slices, ~412 of 12000 tokens
  lexical_anchor   internal/repository/payment.go:1-60 payment.go
  lexical_anchor   internal/service/payment.go:1-60 payment.go
  graph_expansion  internal/repository …
```

Each slice carries its workspace, repository, path, content hash and index
version, and the packet builder rejects anything from another workspace. Add
`--json` to see the full provenance.

## Impact analysis today

```console
$ le graph impact payment.go --change rename
3 consumers: 0 breaking, 0 undetermined, 3 behaviour-only, 0 compatible

  internal/repository            compiles_behaviour_may_differ resolved   via contains     depth 1
                                   → no source change expected; re-run this consumer's tests
  …

A missing edge means 'not discovered', not 'does not exist'. Consumers reached
by inferred or unknown evidence are listed and treated as present.
```

With only containment edges, this reports the directories that contain the
file. Once the Go analyzer lands, the same command on a *function* will report
its callers, its interface implementations, the tests covering it, and the
configuration keys it reads — each with the evidence category that says how the
system knows.

The verdict column will be more interesting then too. The verdicts come from a
fixed table over (change kind, edge kind, evidence), so the same change always
produces the same report — useful at a review gate precisely because it does
not vary.

## Keeping the index fresh

```console
$ le doctor --json | jq '.checks[] | select(.name == "index freshness")'
{
  "name": "index freshness",
  "level": "ok",
  "detail": "last indexed 3m ago; 28 nodes, 27 edges"
}
```

Index units carry a content manifest, a lockfile hash, the toolchain and the
indexer version. A change to any of them marks the unit dirty, and dirty units
are re-analysed before any step that needs the graph. Re-run `le index` to do
it immediately.

## What this gets you before the task loop exists

- Structural questions answered from an index rather than from a model.
- Retrieval with full provenance and a hard workspace boundary.
- A ledger ready to record work, and recovery ready to reconcile it.

## Next

- [The graph schema](../reference/graph-schema.md) — what is implemented today
- [Why small models can work here](../explanation/why-small-models.md) — the
  argument the whole design rests on, and how it will be tested
- [ROADMAP.md](../../ROADMAP.md) — where the language analyzers sit
