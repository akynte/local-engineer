# Why small models can work here

This is an argument, not a result. Nothing in this repository yet proves it.
This page states the claim precisely enough that it can be tested, and says how
it will be.

## The claim

A model small enough to run on an 8 GB GPU can do useful engineering work on a
large codebase, if the harness takes on the jobs the model is bad at.

## What small models are actually bad at

Not "coding" in general. Specifically:

1. **Finding the right code.** They do not know a repository's structure and
   cannot search effectively for it.
2. **Holding a lot of context.** Both the window and the attention quality run
   out.
3. **Remembering across sessions.** They restart from nothing.
4. **Knowing whether they succeeded.** They are confident either way.
5. **Staying on task over many steps.** They drift.

Each has a deterministic answer that does not need a model at all.

## The answers

**Finding code** is a graph query. Callers, implementations, consumers of a
config key, tests covering a function — all of these are index lookups, not
inference. The retrieval order is fixed: lexical anchors first, then graph
expansion from those anchors. The model does not choose what it sees.

**Context size** stops mattering when the packet is assembled rather than
stuffed. Signatures first, bodies on demand. Consumers and contracts occupy
mandatory slots so they are never dropped when the budget is tight. The window
bounds one step, not the task.

**Memory across sessions** is the journal: files investigated, decisions
accepted, hypotheses rejected *with the evidence that ruled them out*, edits
completed, validations run and which are stale. A resumed session is seeded
from that, not from a chat summary — a summary loses exactly the facts you
need.

**Knowing whether it succeeded** is the compiler, the type checker, the tests,
the race detector, the linters, the project's own analyzers. Their output is
stored as content-addressed evidence against the candidate hash it was produced
for. "It works" is not a model claim here; it is an artifact with a hash.

**Drift** is bounded by decomposition into child tasks with executable
acceptance criteria, and by the fact that each step's context is assembled
fresh rather than accumulated.

## Why a small window can be an advantage

A large window invites stuffing, and stuffing costs prefill time on every
single step — which on this hardware is the dominant cost. It also dilutes
attention across text that is not relevant.

A small window forces the retrieval to be right. When it is wrong, that shows
up as a measurable retrieval miss rather than as a model that quietly ignored
the relevant paragraph.

## What is explicitly rejected

- **Whole-repository summaries generated up front.** Cost without measured
  value.
- **Asking the model to compress its own history.** Loses facts. The journal
  replaces it.
- **Ever-larger context windows.** Prefill cost on this hardware, and
  diminishing attention quality.
- **Personas.** Several prompts against the same weights is not several
  engineers. Kept as context recipes, dropped as a metaphor.
- **"Think harder" reflection with no new evidence.** If nothing new has been
  observed, another pass over the same information is not more likely to be
  right.

## How the claim will be tested

For each task category, the accepted-task rate within a fixed budget for:

- **(a)** the same local model driven directly, with no supervisor,
- **(b)** the supervised system,
- **(c)** a frontier agent, on a non-sensitive task set.

(a) is the one that matters. It isolates the harness from the model: if (b) is
not clearly better than (a), the harness is not earning its complexity.

The task set, the hardware, the model manifest, the budget and the scripts will
be published so anyone can reproduce or dispute the numbers.

The graph's contribution specifically will be measured by ablation — running
with and without it — not assumed. That is a deliberate response to a
literature where structural retrieval is often asserted rather than isolated.

## What the README will claim

Only what `docs/benchmarks/results/` shows. Until those numbers exist, the
README claims nothing about success rates, and this page is labelled as an
argument.

## Where the design could be wrong

Worth stating plainly:

- Small models may fail at *synthesis* — writing the change once it has been
  localised — in a way no harness fixes.
- The graph may not help beyond good lexical search. The ablation is there to
  find out.
- Deterministic verification catches "does not compile" and "test fails". It
  does not catch "compiles, passes, wrong". Runtime verification and review
  address part of that, and nothing addresses all of it.
- The harness has a cost of its own: every retrieval, every journal write,
  every verification run. If the model is good enough to not need them, they
  are pure overhead.

These are why the comparison against (a) is the primary measurement rather than
an afterthought.
