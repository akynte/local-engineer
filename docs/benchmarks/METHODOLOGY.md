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

## What these benchmarks do not tell you

They measure the deterministic layer only — the part that runs without a model.
They say nothing about task success rates, and nothing about whether the
supervised system produces better results than the same model unsupervised.

That comparison is the one that actually matters, and it is not implemented
yet. When it is, it will compare, per task category and within a fixed budget:

- **(a)** the local model driven directly, with no supervisor,
- **(b)** the supervised system,
- **(c)** a frontier agent on a non-sensitive task set,

with the task set, model manifest, budget and scripts published. Until those
numbers exist in `results/`, the README claims nothing about success rates.

## Model throughput

`le models bench` measures a model on *your* machine and writes a hardware
profile from what it observes. Those numbers are not comparable across
machines and are not published here; they are inputs to your own configuration.

One honesty note carried into the generated profile: the split between prefill
and decode rate is **apportioned from per-request totals**, not measured with
token-level timings. The profile description says so, so nobody reads those two
numbers as more precise than they are.
