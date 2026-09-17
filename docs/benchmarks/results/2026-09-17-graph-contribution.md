# Graph contribution — 2026-09-17

The pre-registered re-run of
[does the code graph earn its cost?](../PREREGISTRATION-graph-contribution.md),
after the retrieval defect in
[074bba8](https://github.com/akynte/local-engineer/commit/074bba8) was fixed and
both wall-clock ceilings raised.

## The answer

**The graph does not earn its cost on this set.** By the criteria fixed before
the run, this is the "does not" branch.

| Arm | Solved | False accept | Hidden tests | Median |
|---|---|---|---|---|
| `supervised` | 54% [38–70%] (19/35) | 23% [12–39%] (8/35) | 76% | 2m48s |
| `supervised-no-graph` | 51% [36–67%] (18/35) | 20% [10–36%] (7/35) | 75% | 3m5s |

Primary outcome, paired McNemar exact over the 35 shared runs: the arms
disagreed on 11 of them, **6–5**, **p = 1.00**. That is not a near miss. It is
the most symmetric result the test can produce.

Secondary outcome, paired bootstrap on the graded score: **+1.0 points, 95%
[−10.2, +11.7]**, which includes zero.

## What did change: the graph stopped costing anything

The defect was real and fixing it moved a real number.

| | Previous run | This run |
|---|---|---|
| Median tokens, `supervised` | 599,514 | 240,009 |
| Median tokens, `supervised-no-graph` | 338,330 | 263,248 |
| Graph's token cost | **1.77×** | **0.91×** |
| Median wall clock, `supervised` | 4m51s | 2m48s |

Expansion previously took whatever budget the anchors left — measured at 2,920
tokens of a 4,000-token packet. Bounded to a third and ordered by the relevance
score that was being computed and discarded, `supervised` now spends *fewer*
tokens than the arm without a graph and finishes faster.

So the earlier "1.8× for nothing" was two findings wearing one coat: a genuine
defect, and a null result underneath it. Removing the defect did not reveal a
hidden benefit. It revealed the null more clearly.

## Confounds, checked rather than assumed

The previous run scored the arms partly on speed: 8 of 25 `supervised` runs hit
a ceiling against 5 of 25 without the graph. With both budgets raised:

| Arm | At a ceiling |
|---|---|
| `supervised` | 5/35 |
| `supervised-no-graph` | 3/35 |

Close enough that the pre-registration's void condition does not trigger, and in
any case the arms now differ by 3 points on an outcome where the clock is not
what separates them.

## What this does not establish

**11 of 14 task/arm cells changed verdict between the five passes.** That is
worse instability than the run before it, and it is the most important number on
this page. A set this unstable can detect a large effect and not much else.

Seven tasks. Four declare three or more packages in scope, which is where the
graph's stated case lives — impact analysis across a package boundary that the
compiler finds only after a change is made. Three do not, and dilute it.

So the honest reading is not "the graph is worthless". It is: **on seven tasks,
against a 35B local model, with cells that disagree with themselves three times
in four, this set cannot detect a contribution — and the effect, if there is
one, is bounded at roughly ±11 points.** The roadmap entry stays open.

What would move it is more tasks of the shape the mechanism is for, not more
repetitions of these. The bound is already tight enough that repetition is not
the binding constraint.

## Reproducing

```console
$ le eval run --tasks evals/tasks \
    --task conflict-status-001,damaged-stock-001,discount-floor-001,\
perishable-zone-001,reconcile-steals-reservations,reserve-idempotent-001,\
restock-silence-001 \
    --arms supervised,supervised-no-graph --repeat 5 --out results.json
$ le eval report results.json --tasks evals/tasks
```

Hardware and model as disclosed in
[2026-09-14-tasks.md](2026-09-14-tasks.md). 70 runs, 6h16m.
