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

## 2026-09-14-storage.md

### Storage and graph benchmarks — 2026-09-14
First committed results, and the reason for a change to the traversal
implementation.

Full result: [`docs/benchmarks/results/2026-09-14-storage.md`](docs/benchmarks/results/2026-09-14-storage.md)

## 2026-09-14-tasks.md

### Task-success results — 2026-09-14
`le eval run` over 3 tasks × 4 arms, 5 passes, 60 runs, against a local model on the hardware below.

Full result: [`docs/benchmarks/results/2026-09-14-tasks.md`](docs/benchmarks/results/2026-09-14-tasks.md)

---

Regenerate with `make benchmarks`. CI fails if this file is out of date
with the results it summarises, so a published snapshot cannot drift away
from the measurements it claims to describe.
