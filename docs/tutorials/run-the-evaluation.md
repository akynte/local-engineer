# Run the reference evaluation

Measure this system against a task set on your own hardware, and get numbers you
can defend.

**Time:** about two hours for a full run on an 8 GB card. Most of that is the
model working.

## Before you start

You need a model configured and reachable:

```console
$ le models health
local                ok
```

If that fails, do [configure models](../how-to/configure-models.md) first.

Then measure the machine, because the packet sizes the run uses come from a
profile and a guessed profile measures the guess:

```console
$ le models bench --write
```

## What the arms are

```console
$ le eval arms
```

Four configurations, each removing one thing:

| Arm | What it removes |
|---|---|
| `unsupervised` | everything: no retrieval, no graph, no verification loop |
| `supervised` | nothing — the full pipeline |
| `supervised-no-graph` | graph expansion and impact analysis |
| `supervised-no-verification` | the compiler-and-test correction loop |

The ablations are structural rather than a flag the pipeline might ignore: an
arm without retrieval is built without a retriever, and is not offered the tools
it could not run.

## Run it

```console
$ le eval run \
    --arms unsupervised,supervised,supervised-no-graph,supervised-no-verification \
    --repeat 5 \
    --out results.json
```

`--repeat` is not optional in spirit. Two consecutive runs of an earlier task
set disagreed on 4 of 12 cells: a single run of a cell is one sample, not a
measurement of it, and the report says so when you leave it out.

## Read it

```console
$ le eval report results.json
```

Three things to read before any percentage:

**The caveats, first.** They are generated from what actually ran. If cells
changed verdict between passes, that governs how much weight anything else can
carry.

**False acceptance, beside every solved rate.** It is the system claiming
success on work that failed the hidden test — the most damaging failure this
kind of tool has, which is why it is never folded into the solved rate.

**Whether the intervals overlap.** The report says "no detectable difference"
when they do, and that is the honest answer even when the gap looks large.

## Why the numbers can be trusted

The acceptance tests are **never in the worktree while a task runs**. They are
copied in afterwards. A model cannot satisfy a test it can read, and a task set
that let it would measure reading.

The system's own verdict is recorded **separately** from the ground truth, so
"claimed success and was wrong" is its own number rather than something averaged
away.

## Publishing

If you publish results, disclose what
[`docs/benchmarks/results/README.md`](../benchmarks/results/README.md) requires: exact
hardware, the model and its quantisation, the commit, each task's leak risk, the
full raw output, confidence intervals, and the false-acceptance rate. A result
missing any of those cannot be reproduced or disputed, which is the only reason
to publish one.
