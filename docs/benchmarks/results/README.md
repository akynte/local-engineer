# Results

Committed evaluation results live here, one file per run, with the hardware and
the model manifest disclosed.

## What is here

| File | What it measures |
|---|---|
| [`2026-09-14-storage.md`](2026-09-14-storage.md) | Storage and graph latency on the reference laptop |

## What is not here

**No task-success results.** The evaluation harness is built and tested
(`internal/eval/`, `evals/tasks/`), and the task set is validated on every CI
run — but no run against a real model has been done, so there is nothing to
publish.

This is stated rather than left to inference because the absence is easy to
misread. A repository containing an evaluation harness looks like a repository
with evaluation results. It does not have them.

Until a file here says otherwise:

- the README claims nothing about task success rates,
- no comparison against a frontier agent has been made,
- the graph's contribution has **not** been measured, only made measurable.

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
