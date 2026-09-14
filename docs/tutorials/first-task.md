# First task in 15 minutes

By the end of this you will have a running supervisor, an indexed repository,
and a graph you can ask questions of. You will not need a model for any of it:
indexing, the graph and impact analysis are all deterministic.

## What you need

Docker Engine 24 or later. Nothing else — no Go, no Node, no Python on your
host.

## 1. Start the container

```console
$ docker volume create le-data
$ docker run --rm -v le-data:/data alpine chown -R 10001:10001 /data
$ docker run -d --name local-engineer \
    -v le-data:/data \
    -v "$HOME/code":/work \
    -p 127.0.0.1:7777:7777 \
    ghcr.io/akynte/local-engineer:latest-cpu
```

A fresh named volume is created owned by root; the `chown` is a one-time step
because the container runs as an unprivileged user.

Publish on `127.0.0.1`. The supervisor can run sandboxed commands and read
every repository you index, so it must not be reachable from your network.

## 2. Check what is actually in effect

```console
$ docker exec local-engineer le doctor
```

You will see something like:

```
[ok  ] container boundary (DR-3 layer 1)  /.dockerenv is present
[ok  ] landlock (DR-3 layer 2)            ABI 6; TCP rules enforced
[warn] bubblewrap (DR-3 layer 3)          bwrap probe failed: No permissions to create new namespace
[ok  ] data directory                     /data on ext4
[ok  ] hardware profile                   reference-8gb-cuda-64gb-ram: context 32768, packet cap 12000
```

The bubblewrap warning is normal: unprivileged user namespaces are usually
unavailable inside a container, so the optional third isolation layer is off.
[The isolation model](../explanation/isolation-model.md) explains exactly what
you still get.

`le doctor` exits 0 when everything is clean, 1 on warnings, 2 on failures. It
is the first thing to run when something is wrong, and the first thing to
attach to a bug report.

## 3. Create a workspace

A **workspace** is the unit of isolation. Everything the system learns about a
project lives under that workspace's id and nowhere else.

<!-- test:run -->
```console
$ mkdir -p demo/internal/service && cd demo
$ printf 'module example.com/demo\n\ngo 1.26\n' > go.mod
$ printf 'package service\n\ntype UserService struct{}\n\nfunc (s *UserService) GetUser(id string) string { return id }\n' > internal/service/user.go
$ le workspace init --name demo
workspace …
  name:         demo
  root:         …/demo
  pinned in:    …/demo/.le/workspace.yaml
  state under:  …
```

The id is written to `.le/workspace.yaml`. **Commit that file.** It pins the
identity to the code, so the workspace survives moving the directory, and
`le workspace adopt` re-binds it after a move.

## 4. Index the repository

<!-- test:run -->
```console
$ le index
demo: … files, … chunks, … nodes, … edges …
```

This walks the repository and writes files, directories, containment edges and
lexical chunks into the workspace's own `index.db`. Nothing about this project
touches any other workspace's data.

## 5. Ask the graph a question

<!-- test:run -->
```console
$ le graph stats
… nodes, … edges

by relationship:
  contains       …

by evidence category:
  resolved       …
```

Every edge carries an **evidence category**: how the system knows the
relationship exists. `resolved` means a compiler, type checker or the
filesystem said so. `inferred` means a heuristic found it. The distinction
matters at the next step.

## 6. Ask what a change would break

<!-- test:run -->
```console
$ le graph impact user.go --change rename
… consumers: … breaking, … undetermined, … behaviour-only, … compatible
```

The report lists every consumer with its evidence category, a compatibility
verdict, and the migration step it needs. The verdicts come from a
deterministic table over the change kind and the edge kind — no model is
involved, so the answer is the same every time.

It always ends with:

```
A missing edge means 'not discovered', not 'does not exist'.
```

That is not boilerplate. The graph reports what analysis found; absence of an
edge is absence of evidence, and the report says so every time rather than
letting you forget it.

## 7. Retrieve context the way a task step would

<!-- test:run -->
```console
$ le graph search "UserService"
… slices, ~… of … tokens
  lexical_anchor   internal/service/user.go…
```

This is the retrieval order the supervisor uses for every step: lexical anchors
first, then graph expansion from those anchors, then mandatory slots filled by
impact analysis so consumers and contracts are never dropped. The model only
ever sees the result.

## What you have now

- A workspace whose index, ledger, telemetry, cache and artifacts are separate
  files in a separate directory from every other project.
- A queryable graph with evidence categories.
- Deterministic impact analysis.

## Next

- [Configure a model](../how-to/configure-models.md) so tasks can actually run.
- [Choose a hardware profile](../how-to/choose-a-profile.md) — the shipped ones
  are starting points, not measurements, and `le doctor` warns until you
  measure your own machine.
- [Why small models can work here](../explanation/why-small-models.md) if you
  want the argument behind the design.
