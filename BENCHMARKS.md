# Benchmarks

A snapshot of what has actually been measured, assembled from the result files
under [`docs/benchmarks/results/`](docs/benchmarks/results/). Every release
attaches this file.

It is assembled from committed results rather than regenerated at release time:
a release is built on a shared cloud runner, and numbers from one are not a
snapshot of any reference configuration. Each result below discloses the machine
it came from.

**Read the caveats in each result file before quoting a number from it.** The
task-success results in particular settle nothing: every arm's confidence
interval overlaps every other's.

## What has been measured

| Result | Measured |
|---|---|
| [`2026-09-14-storage.md`](docs/benchmarks/results/2026-09-14-storage.md) | Storage and graph benchmarks — 2026-09-14 |
| [`2026-09-14-tasks.md`](docs/benchmarks/results/2026-09-14-tasks.md) | Task-success results — 2026-09-14 |
| [`2026-09-15-packet-cap.md`](docs/benchmarks/results/2026-09-15-packet-cap.md) | Packet cap by needle test — no retrieval ceiling at any size this hardware can serve |
| [`2026-09-17-graph-contribution.md`](docs/benchmarks/results/2026-09-17-graph-contribution.md) | Graph contribution — 2026-09-17 |

## 2026-09-14-storage.md

### Storage and graph benchmarks — 2026-09-14
First committed results, and the reason for a change to the traversal
implementation.

Full result: [`docs/benchmarks/results/2026-09-14-storage.md`](docs/benchmarks/results/2026-09-14-storage.md)

## 2026-09-14-tasks.md

### Task-success results — 2026-09-14
`le eval run` over 3 tasks × 4 arms, 5 passes, 60 runs, against a local model on the hardware below.

Full result: [`docs/benchmarks/results/2026-09-14-tasks.md`](docs/benchmarks/results/2026-09-14-tasks.md)

## 2026-09-15-packet-cap.md

### Packet cap by needle test — no retrieval ceiling at any size this hardware can serve
A 35B MoE recalled a random access code at **every depth of every packet size
tested, from 8,000 to 64,028 tokens**, across two server configurations. The
context window was doubled from 32,768 to 65,536 specifically to look for the
point where retrieval degrades. **It was not found.** Fifty probes, no misses.

Full result: [`docs/benchmarks/results/2026-09-15-packet-cap.md`](docs/benchmarks/results/2026-09-15-packet-cap.md)

## 2026-09-17-graph-contribution.md

### Graph contribution — 2026-09-17
The pre-registered re-run of
[does the code graph earn its cost?](../PREREGISTRATION-graph-contribution.md),
after the retrieval defect in
[074bba8](https://github.com/akynte/local-engineer/commit/074bba8) was fixed and
both wall-clock ceilings raised.

Full result: [`docs/benchmarks/results/2026-09-17-graph-contribution.md`](docs/benchmarks/results/2026-09-17-graph-contribution.md)

---

Regenerate with `make benchmarks`. CI fails if this file is out of date
with the results it summarises, so a published snapshot cannot drift away
from the measurements it claims to describe.
