# Results

Committed evaluation results live here, one file per run, with the hardware and
the model manifest disclosed.

## What is here

| File | What it measures |
|---|---|
| [`2026-09-14-storage.md`](2026-09-14-storage.md) | Storage and graph latency on the reference laptop |
| [`2026-09-14-tasks.md`](2026-09-14-tasks.md) | Task success across four arms, 60 runs, local 35B MoE |
| [`2026-09-15-packet-cap.md`](2026-09-15-packet-cap.md) | The needle test (§8.3): no retrieval ceiling at any size this hardware can serve |

## What the task results do and do not show

The task-success file is a real run: 3 tasks × 4 arms × 5 passes against a local
model, with the hidden acceptance tests never in the worktree while a task ran.

It settles nothing about the design. **Every arm's confidence interval overlaps
every other's**, so no comparison in it is statistically detectable, and 4 of the
12 task/arm cells changed verdict between passes — a cell that disagrees with
itself has not been measured. Two of the three tasks were solved by every arm on
every pass, including the baseline with no retrieval, no graph and no
verification, so they carry no information about the pipeline.

What it does establish is narrower and worth having:

- the harness runs end to end against a local model, and the completion contract
  accepts and rejects on evidence it actually gathered;
- false acceptance is real and measurable — the unsupervised baseline claimed
  success on work that failed the hidden test in 4 of 15 runs;
- the task set is too small and too easy to answer the questions the arms were
  built to ask, which is a fact about the set, not about the system.

Until a larger set says otherwise:

- the README claims no task-success rate,
- no comparison against a frontier agent has been made,
- the graph's contribution is **measurable but not yet measured to a
  conclusion**: on this set it changed the solved rate by zero points and
  roughly doubled the tokens spent on the only task that discriminated.

## What a published result must carry

Every result file discloses, without exception:

- the exact CPU, RAM, GPU and kernel,
- the model, its quantisation and the hardware profile used,
- the commit,
- the task set and each task's leak risk,
- the full raw output, not just the summary,
- the confidence interval on every rate,
- the false-acceptance rate beside every solved rate.

A result missing any of these is not publishable here. The point of the file is
that someone else can reproduce or dispute it.

## Reproducing

```console
$ le eval run --arms unsupervised,supervised,supervised-no-graph --out results.json
$ le eval report results.json
```

See [the methodology](../METHODOLOGY.md) for what each arm isolates and how to
read the numbers.
