# C4 level 2: containers

What runs, and where the data lives.

```mermaid
C4Container
  title Container view — the default single-container deployment

  Person(dev, "Developer")

  Container_Boundary(box, "local-engineer container") {
    Container(sup, "le (supervisor)", "Go, PID 1", "Process manager, HTTP API, dashboard, ACP bridge. Owns the ledger and every decision.")
    Container(engine, "Editing engine", "Go, in-process", "A bounded tool loop over the provider boundary, confined to one worktree.")
    Container(llama, "llama-server", "C++, child process", "Only when inference.mode is embedded. Started with the active profile.")
    Container(sidecar, "TypeScript sidecar", "Node, on demand", "Compiler-backed TypeScript analysis. Optional; the Go analyzer reads lexically without it.")
    ContainerDb(index, "index.db", "SQLite", "Files, nodes, edges, chunks, FTS. One file per workspace.")
    ContainerDb(ledger, "ledger.db", "SQLite, synchronous=FULL", "Tasks, the intent-first journal, checkpoints, evidence, leases.")
    ContainerDb(telem, "telemetry.db", "SQLite", "Counters and samples. Never content.")
  }

  System_Ext(repo, "Repositories", "Bind-mounted at /work")
  System_Ext(ext, "External inference", "When inference.mode is external")

  Rel(dev, sup, "CLI and HTTP", "127.0.0.1:7777")
  Rel(sup, engine, "Runs one task at a time")
  Rel(engine, llama, "Chat and tool calls", "HTTP")
  Rel(engine, ext, "Same API, different host", "HTTP")
  Rel(sup, index, "Retrieval and graph queries")
  Rel(sup, ledger, "Intent before the side effect, outcome after")
  Rel(sup, telem, "Counters")
  Rel(engine, repo, "Edits a per-task worktree, never the checkout")
  Rel(sup, sidecar, "Indexes TypeScript", "stdout JSON")

  UpdateLayoutConfig($c4ShapeInRow="3", $c4BoundaryInRow="1")
```

## Why one container (DR-1)

The conventional layout is one process per container so the orchestrator can see
each. Here the supervisor **already is** a process manager with a ledger, so
putting the children under it keeps one lifecycle and one health endpoint. The
trade-off is stated rather than hidden: several processes under one PID 1 reduce
orchestrator visibility.

`deploy/docker-compose.split.yml` is the conventional layout for anyone who
wants it, and it is exercised in CI against a stub inference service — not
merely parsed — because the claim it makes is that pointing the supervisor at an
external inference container is a configuration change only.

## Why three databases, not one

§5.2: a large index rebuild must never block the ledger, and the ledger must be
backed up independently at high frequency. They also have different durability
needs — `synchronous=FULL` for the ledger, `NORMAL` for the other two — and one
file cannot have two settings.

## Where the boundaries actually are

The worktree is the one that matters. The engine never edits the developer's
checkout: it works in a per-task worktree, and an accepted change is applied
through a gate. A crash therefore cannot leave the developer's files half-edited,
which is what makes the recovery procedure worth having.

Next: [component view](c4-component.md).
