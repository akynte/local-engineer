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
| [`2026-09-15-packet-cap.md`](docs/benchmarks/results/2026-09-15-packet-cap.md) | Packet cap by needle test — no retrieval ceiling below the context window |

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

### Packet cap by needle test — no retrieval ceiling below the context window
A 35B MoE recalled a random access code at every depth of every packet size
tested, from 8,000 to 32,024 tokens, on a machine whose per-slot context window
is 32,768. **No retrieval ceiling was found** — recall was perfect to within
744 tokens of the window's edge, which is as close as the sweep can get. The measurement's own conclusion
is therefore a negative one: at this context size the packet cap is bounded by
the window, not by what the model can retrieve from, and
`max_packet_tokens: 16384` is well inside what the model demonstrably handles.

Full result: [`docs/benchmarks/results/2026-09-15-packet-cap.md`](docs/benchmarks/results/2026-09-15-packet-cap.md)

---

Regenerate with `make benchmarks`. CI fails if this file is out of date
with the results it summarises, so a published snapshot cannot drift away
from the measurements it claims to describe.
