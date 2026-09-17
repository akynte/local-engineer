# Benchmark methodology

Every performance number this project publishes comes from a script in this
repository, run on disclosed hardware, with the raw output committed. Nothing
is quoted from memory or from a single lucky run.

## What is measured, and why

### Storage and graph (`evals/storage/`)

These exist because [DR-1](../adr/0001-single-container-sqlite.md) and
[DR-2](../adr/0002-graph-in-sqlite.md) chose SQLite over alternatives, and both
records name a disadvantage that is a performance claim. A decision record that
rests on "it should be fast enough" is an opinion; these numbers are what make
it a decision.

| Benchmark | What it tells us |
|---|---|
| `SymbolLookup` | Whether "answer structural questions outside the model" is interactive |
| `ThreeHopTraversal` | Whether DR-2's shallow-traversal commitment holds at scale |
| `ImpactAnalysis` | Whether impact analysis can run before every edit |
| `FTSQuery` | Whether the lexical-anchor stage is free relative to inference |
| `VectorScan` | Whether brute-force scanning is viable, or an index is needed |
| `LedgerAppend` / `LedgerCheckpoint` | What intent-first journalling costs per action |

### The synthetic graph

Nodes and edges are generated with a **Zipf** in-degree distribution, not a
uniform one. Real call graphs are heavy-tailed: a few utility symbols have
thousands of callers and most have a handful. A uniform random graph would make
traversal look considerably faster than it is, because the hot nodes are
exactly the ones a reverse impact walk lands on.

Sizes are 10⁵ and 10⁶ edges, matching §5.3 of the design.

## Running them

```console
$ make bench
$ go test -run '^$' -bench . -benchmem -count 6 ./evals/... | tee new.txt
$ benchstat old.txt new.txt
```

CI runs the same benchmarks on pull requests that touch storage, graph or
retrieval, and posts a `benchstat` comparison against the base branch.

## How results are reported

**As a diagnostic, never as a gate.** CI runners are noisy and shared; a
change under roughly 10% is not distinguishable from noise there. A regression
is investigated by reproducing it on disclosed hardware, not by trusting a CI
delta.

Committed results in `results/` always disclose:

- the exact CPU, RAM, GPU and kernel,
- the commit,
- the Go version,
- the filesystem the data directory was on (it matters: see §5.4),
- the full raw `go test -bench` output.

## Task success (`evals/tasks/`, `internal/eval/`)

This is the measurement that actually matters, and the one the README's claims
are gated on.

### What is compared

| Arm | What it isolates |
|---|---|
| `unsupervised` | The same local model given the objective and the worktree — no retrieval, no graph, no verification loop |
| `supervised` | The full system |
| `supervised-no-graph` | The full system with graph expansion and impact analysis off, lexical retrieval left on |
| `supervised-no-verification` | The full system with the compiler-and-test correction loop off |
| `frontier` | A hosted model through the same pipeline, to calibrate task difficulty |

`le eval arms` prints these with the question each pairing answers.

**`unsupervised` is the comparison this project is judged on.** If the
supervised system is not clearly better than the same model driven directly,
the harness is not earning its complexity, and no result against a frontier
agent changes that.

The two ablation arms exist because §3.1 requires measuring the graph's
contribution "with an ablation, not by assumption". An ablation that leaves the
ablated component partly running measures nothing, so the arms differ
structurally — a different retriever, a different recipe set — rather than by a
flag the pipeline might ignore.

### What makes a result trustworthy

**Acceptance is hidden.** A task's tests are never in the worktree while the
task runs; they are written in afterwards. A model that can read the test can
satisfy it without solving the problem, and that failure looks exactly like
success in the numbers.

**Ground truth is separate from the system's own verdict.** The pipeline
decides acceptance from the evidence it gathered; the harness decides it from
the hidden tests. When those disagree in the system's favour that is a **false
acceptance** — it claimed success and was wrong — and it is reported as its own
rate. A solved rate should never be read without it.

**Protected paths are checked.** A task "passed" by deleting the failing test
is not solved. Tasks declare `must_not_change`, and a run that touches one is
counted as unsolved.

**A harness fault is not a failed task.** A run that errors is excluded from
every rate and counted separately; treating a crashed runner as evidence about
the system would understate it.

**Provenance is disclosed per task.** A task the model saw in training gives an
inflated number. Every task declares its leak risk, and a report mixing risks
says so in its caveats.

### What makes the statistics honest

Rates are reported with a 95% Wilson interval, not as bare percentages. Wilson
rather than the normal approximation because at small samples and rates near 0
or 1 the normal interval produces bounds outside [0,1] — which is exactly the
regime this set sits in.

**Comparisons between arms are paired, not independent.** Every arm is given
the same tasks on the same passes, so the dominant source of variance — one
task simply being harder than another — is shared between the arms and cancels
when the comparison is made within a task. Comparing two Wilson intervals
throws that away: it asks whether two independently drawn rates differ, which
is a question about a design this evaluation does not have, and it answers
"cannot tell" long after the data could tell.

The test is **McNemar's, exact**, because the disagreeing counts here are
single digits and the chi-squared approximation is not trustworthy there.
Pairs where both arms solved a task, or neither did, carry no information about
which arm is better and are excluded by construction; only the runs where the
arms disagree can move a verdict. A run that errored drops its whole pair — a
harness fault is not evidence about either arm, and keeping the pair would
score the fault as a loss for whichever side happened to run.

Every verdict states the evidence it rests on: how many shared runs the arms
disagreed on, which way, and the exact p. "Better by 20 points" over three
disagreements and over thirty are different claims, and a reader shown only the
gap cannot tell them apart. It also tells you whether more runs would help — a
comparison that splits 3–5 over 25 shared runs has a small effect, not an
unmeasured one, and no affordable number of repetitions will resolve it.

**Runs are graded, not just passed or failed.** Solved stays the headline and
stays binary: a task is done or it is not, and partial credit is not an outcome
anyone can ship. But a binary result carries one bit per run, and separating two
arms that differ slightly then needs more runs than the hardware can produce in
a working day. Each task's hidden acceptance is already several independent
assertions, so the harness records how many of them held. One bit becomes
several, from exactly the same run.

The count comes from the task, not the output: `go test` names its failures and
says nothing about what passed, so the set of hidden tests is read from the
task's own acceptance files and the failures are subtracted from it. A failing
run that names no hidden test did not reach them — a build error or a timeout —
and scores zero, because the alternative is awarding full marks to the code that
compiles least.

Arms are then compared on the mean per-pair difference in score, with a
bootstrap interval over the pairs. Bootstrap because scores are bounded,
discrete and skewed, which is the regime where a normal interval is wrong. The
graded comparison answers a narrower question than Solved does — "did this arm
get further" is not "did this arm do the job" — so it is reported beside the
binary test and never in place of it. What it buys is resolution: on the first
large-fixture run it turned "the arms disagreed on 8 of 25 runs, p=0.73" into an
effect of -4.7 points bounded at [-17.3, +8.0], and showed that the bare
baseline, which solved nothing at all, was still satisfying 40% of the hidden
tests.

**Analysis is re-derived from the outcomes, never read back from the file.**
`le eval report` recomputes the arms, the comparisons and the caveats from the
saved runs every time. The outcomes are the measurement and they do not change;
everything else is a reading taken from them, and a reading that improves
should improve for runs that have already been paid for. Eight GPU-hours should
not have to be spent again to apply a better test to them.

### Running it

```console
$ le eval tasks                       # list and validate the set
$ le eval arms                        # what each configuration isolates
$ le eval run --arms unsupervised,supervised --out results.json
$ le eval report results.json
```

`--repeat` defaults to 3. A single pass of a cell is one sample, and the first
real run changed verdict on 4 of 12 cells between passes, so a one-pass table
reports a stability it never measured. Budget accordingly: one supervised task
on the 2,075-line fixture took 7m23s on the reference hardware, so a set of *n*
tasks over *a* arms at *r* repetitions is roughly `n × a × r × 7` minutes.

### The task set

Tasks live in `evals/tasks/` as `*.task.yaml` with a fixture directory. Each
carries an objective phrased as a user would phrase it, a scope, a budget, and
hidden acceptance files.

`internal/eval/taskset_test.go` checks every task two ways on every CI run:

- the acceptance command must **fail** on the untouched fixture, or the task
  measures nothing;
- a reference solution must **pass**, or the task is unsolvable and every arm
  scores zero for reasons unrelated to the system.

A task with no reference solution fails the suite. It also checks that fixtures
start green on their own visible tests, and that objectives do not name the
fix — an objective that says what to change measures typing, not engineering.

### Status: run once, and the run settled nothing

**Task-success numbers exist and are published**:
[2026-09-14-tasks.md](results/2026-09-14-tasks.md), 60 runs against a local 35B
MoE on disclosed hardware. Read them with what they do not show attached.

The run's own conclusion is that the task set was not up to the job. Every arm's
confidence interval overlapped every other's; 4 of 12 task/arm cells changed
verdict between passes; and two of the three tasks were solved by every arm on
every pass, including the bare baseline, so they carried no information. The
fixtures were 17 to 87 lines of Go — small enough that reading the whole
repository fits in one packet, which leaves retrieval and the graph nothing to
contribute by construction.

So the README still claims no success rate, and **the graph's contribution
remains measurable rather than measured**. A 2,075-line fixture and five tasks
over it now exist to answer that; whether they discriminate has not been
measured yet. A harness that has produced numbers is not the same as a harness
that has produced evidence, and this section will say so until it is.

## Model throughput

`le models bench` measures a model on *your* machine and writes a hardware
profile from what it observes. Those numbers are not comparable across
machines and are not published here; they are inputs to your own configuration.

One honesty note carried into the generated profile: the split between prefill
and decode rate is **apportioned from per-request totals**, not measured with
token-level timings. The profile description says so, so nobody reads those two
numbers as more precise than they are.
