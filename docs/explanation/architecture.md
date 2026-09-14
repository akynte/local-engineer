# Architecture

## The shape of the idea

A local model that fits on an 8 GB GPU cannot hold a large codebase in its
head, and cannot be trusted to remember what it did an hour ago. The design
does not try to fix either. It moves both problems outside the model.

Every question about the repository is answered *before* the model is
involved — by a typed graph, a lexical index and a journal, all in SQLite. The
model receives only the slices the current step needs, and its actions are
recorded by the supervisor rather than by itself.

A small context window then stops being the binding constraint. It even helps:
it forces precise retrieval and keeps irrelevant text out.

## C4 level 1: context

```
        ┌──────────┐          ┌────────────────────┐
        │ Developer│─────────▶│  local-engineer    │
        └──────────┘   CLI,   │  (one container)   │
                       HTTP,  └─────────┬──────────┘
                       editor           │
                                        │ reads and edits
                                        ▼
                              ┌────────────────────┐
                              │  Your repositories │
                              └────────────────────┘
                                        ▲
                                        │ optional
                              ┌─────────┴──────────┐
                              │ External inference │
                              │ or a remote model  │
                              └────────────────────┘
```

The developer talks to it through the CLI, the HTTP API, or an editor. It reads
and edits repositories mounted into it. Inference is embedded by default and
can be external.

## C4 level 2: containers

Inside the single container, `le` is PID 1 and supervises:

```
┌──────────────────────── container ─────────────────────────┐
│                                                            │
│  tini ──▶ le (supervisor, process manager, ledger)         │
│            │                                               │
│            ├──▶ le api        HTTP 7777: /healthz /readyz  │
│            │                  dashboard, ACP bridge        │
│            │                                               │
│            ├──▶ llama-server  when inference.mode=embedded │
│            │                  health-checked, restarted    │
│            │                                               │
│            └──▶ opencode serve  per task, inside the       │
│                                 task's sandbox             │
│                                                            │
│  /data  (volume)   /work  (your repositories, bind mount)  │
└────────────────────────────────────────────────────────────┘
```

One container with several processes is not the conventional shape, and the
trade-off is recorded in [DR-1](../adr/0001-single-container-sqlite.md): the
orchestrator loses per-process visibility. The compensation is that the
supervisor *is* a process manager — health checks, restart budgets with
backoff, process-group termination, and `/readyz` reporting per-child state.
`deploy/docker-compose.split.yml` is the conventional layout for anyone who
prefers it, and CI validates it.

## C4 level 3: components

```
  cmd/le ──────────────────────────────────────────────────┐
                                                           │
  internal/api          HTTP surface, dashboard            │
  internal/procman      child supervision, health, backoff │
  internal/doctor       "what is actually in effect"       │
                                                           │
  internal/workspace    identity (§2.1), the pin file      │
  internal/store   ◀──── the ONLY path to persistence      │
    ├── index.db        files, nodes, edges, chunks, FTS   │
    ├── ledger.db       tasks, operations, evidence        │
    └── telemetry.db    counters                           │
                                                           │
  internal/graph        typed graph, traversal, impact     │
  internal/index        analyzers → nodes and edges        │
  internal/retrieval    anchors → expansion → mandatory    │
  internal/cache        keyed by workspace + manifest      │
  internal/artifacts    content-addressed evidence         │
  internal/ledger       intent-first journal, recovery     │
                                                           │
  internal/sandbox      layer selection and guarantees     │
    ├── landlock        per-task path and port rules       │
    └── bwrap           optional namespaces                │
                                                           │
  internal/llm          Provider interface, role routing   │
  internal/config       le.yaml, providers.yaml, profiles  │
```

The arrow into `internal/store` is the important one. Every byte of workspace
state goes through it, and a build-time analyzer enforces that: `sql.Open` and
file writes outside `internal/store` and `internal/artifacts` fail the build.

## How a step actually runs

1. The supervisor answers the structural question from `index.db` — who calls
   this, what implements that, which config keys feed this service. No model.
2. Retrieval assembles a packet: lexical anchors first, then graph expansion
   from those anchors, then mandatory slots filled by impact analysis so
   consumers and contracts are never dropped.
3. Every slice is checked against the active workspace. A foreign slice is
   rejected and reported.
4. The intent is journalled.
5. The model runs, inside the sandbox, seeing only the packet.
6. The outcome is journalled, with the content hash before and after.
7. Deterministic verification runs — compiler, vet, lint, tests, analyzers —
   and its output is stored as content-addressed evidence against the candidate
   hash it was produced for.

Steps 1 to 3 are why a small window is workable. Steps 4 and 6 are why a crash
is survivable. Step 7 is why a claim of success means something.

## Where the intelligence is not

Deliberately, the model does not:

- decide what is in its own context (retrieval does),
- decide whether a change is compatible (a deterministic table does),
- summarise its own history (the journal holds it),
- judge whether its work passed (evidence does).

Each of those is a place where a small model would be unreliable and a
deterministic component is cheap.

## Storage

SQLite, three files per workspace. Structural queries, lexical search over
FTS5, recursive-CTE traversals, a transactional ledger, telemetry, and
content-addressed artifacts. No separate database process.

Three files rather than one so a long index rebuild never blocks the ledger,
and the ledger can be backed up at high frequency on its own.

See [DR-1](../adr/0001-single-container-sqlite.md) and
[DR-2](../adr/0002-graph-in-sqlite.md) for the alternatives and what each costs.

## Isolation

Two separate mechanisms — between projects at the storage layer, around running
code at the sandbox layers. [The isolation model](isolation-model.md) states
exactly what each guarantees and what it does not.

## Model independence

Two configuration boundaries: an OpenAI-compatible provider surface with an
explicit capability declaration, and role routing. Changing model, engine or
vendor is configuration. See
[DR-4](../adr/0004-openai-compatible-provider.md).

Everything that depends on your machine — context limits, packet caps, memory
budgets, thread counts, offload layers, sampling defaults — lives in a hardware
profile, generated from measurement by `le models bench`. None of it is
hardcoded.
