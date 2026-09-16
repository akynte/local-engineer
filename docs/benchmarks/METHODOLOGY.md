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
regime a twenty-task set sits in.

A comparison is called significant only when the two intervals do not overlap.
That is a deliberately weak test: with a set this size, claiming a difference
the data cannot support is the likeliest way these numbers mislead. A 10-point
gap over 20 tasks is reported as "no detectable difference".

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
