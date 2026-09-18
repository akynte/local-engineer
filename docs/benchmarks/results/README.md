# Results

Committed evaluation results live here, one file per run, with the hardware and
the model manifest disclosed.

## What is here

| File | What it measures |
|---|---|
| [`2026-09-14-storage.md`](2026-09-14-storage.md) | Storage and graph latency on the reference laptop |
| [`2026-09-14-tasks.md`](2026-09-14-tasks.md) | Task success across four arms, 60 runs, local 35B MoE |
| [`2026-09-15-packet-cap.md`](2026-09-15-packet-cap.md) | The needle test (§8.3): no retrieval ceiling at any size this hardware can serve |
| [`2026-09-18-phase-cache.json`](2026-09-18-phase-cache.json) | Raw append-only versus rewritten-prefix cache measurements, 12 requests |

## Phase cache baseline, 2026-09-18

Measured with `scripts/bench-phase-cache.py` against the existing local server.
Hardware: Intel i7-13620H (16 logical CPUs), 64,247,064 KiB RAM, RTX 4060 Laptop
GPU (8,188 MiB), Linux 7.0.0-31-generic. Model:
`Qwen3.6-35B-A3B-UD-Q4_K_XL.gguf` from the review's model installation.
llama.cpp revision: `3466812d1f06728effe7c0f3c0671117f461672d`.
Repository base: `9e07fe1c85520c7dda41f8bbed69f3b177a4c22a`, with the architecture
implementation changes uncommitted. No generated hardware profile was applied.

The existing server used `-c 65536 --parallel 1 -ngl 999 --n-cpu-moe 999 -fa on
-ctk q8_0 -ctv q8_0 -b 2048 -ub 512 -t 8 -tb 16 --jinja`. This is a baseline;
the review's MTP/checkpoint settings were not enabled or compared.

| Actual prompt size | Append-only follow-up wall time | Rewritten-prefix wall time | Follow-up cached tokens |
|---|---|---|---|
| About 4.1K tokens | 0.526–0.534 s | 15.9–16.0 s (trials 1–2) | 4,090 / 4,112 |
| About 12.7K tokens | 0.547–0.564 s | 34.6–35.3 s | 12,700 / 12,722 |

Each size/mode has three sequential requests, including its initial request.
These are synthetic function listings, not hidden coding tasks. No task success
or false-acceptance rate is measured. Append-only output was six tokens;
rewritten-prefix output was longer, so total latency is not a controlled decode
comparison. The raw JSON retains server prefill/decode timing and cache counts.
Cache state was not reset between modes; some rewritten 12.7K prompts reused
3,573 tokens. Warm follow-ups reused all but 26 prompt tokens. The sample is too
small for a throughput or quality claim, but demonstrates prefix reuse on this
installed model. It does not establish optimal checkpoint, quantization, or MTP
settings. Wall time is not a time-to-first-token measurement.

Reproduce without changing the running server:

```console
$ python3 scripts/bench-phase-cache.py --sizes 4096 12288 --trials 3 --output /tmp/phase-cache.json
```

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
