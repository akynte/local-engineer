# DR-2: Graph in SQLite edge tables rather than an embedded graph database

- Status: Accepted
- Date: 2026-09-14

## 1. Problem

A queryable code graph supporting two- to four-hop traversals and change-impact
analysis.

## 2. Alternatives

- Kuzu, or one of its forks (LadybugDB).
- DuckDB with DuckPGQ.
- Neo4j as a separate service.
- SQLite recursive CTEs over typed edge tables.

## 3. Evidence

Kuzu was archived by its creator in October 2025 and its community is split
across forks, of which LadybugDB is the active one. Adopting a fork would add a
C++ dependency and fork risk. The graphs here are at most a few million edges,
and the queries the design commits to are shallow: recursive CTEs handle that
range well. Neo4j is a second daemon, which DR-1 has already ruled out for the
default. CodexGraph-style designs that let the model write Cypher make
retrieval quality depend on the model's query skill, which is a poor fit for
small local models — and the design's position is that the graph is a tool the
deterministic layer queries, not a database the model queries.

## 4. Chosen

SQLite edge tables with evidence categories, traversed with recursive CTEs.

## 5. Why

No new dependency, no fork risk, one storage technology to back up and
migrate, and per-workspace isolation for free because each workspace is a
separate file.

## 6. Disadvantages

- **No Cypher.** Traversals are written as SQL, which is more verbose.
- **Deep or analytical graph queries are slower** than a native graph engine.
  The design's commitment to shallow traversals is doing real work here; if
  that commitment ever breaks, this decision needs revisiting.
- **Adjacency updates are manual.** There is no engine maintaining indexes for
  traversal; the edge indexes are hand-written and must be kept in step with
  the query shapes.
- The traversal depth is capped in code (`graph.MaxTraversalDepth`), which is
  an honest limit but still a limit.

## 7. Replacement path

`internal/graph` is an interface. `graph.New` returns the SQLite
implementation, and every caller depends only on the interface. The benchmark
harness in `evals/graph/` measures traversal latency at realistic sizes; if it
shows the SQLite implementation failing at a size that matters, those numbers
are the evidence for a swap, and the swap touches one package.
