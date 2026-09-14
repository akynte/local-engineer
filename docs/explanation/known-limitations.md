# Known limitations

What this does not do, stated in one place so nobody has to infer it.

`le doctor` reports the runtime ones at startup.

## The evaluation settles nothing yet

A real run exists — 3 tasks × 4 arms × 5 passes, 60 runs against a local model
on disclosed hardware — and its own conclusion is that the task set cannot
answer the questions the arms were built to ask:

- every arm's confidence interval overlaps every other's;
- 4 of 12 task/arm cells changed verdict between passes, so a cell disagrees
  with itself;
- two of three tasks were solved by every arm on every pass, including the bare
  baseline, so they carry no information.

A larger fixture and five harder tasks now exist, and a probe over them shows
separation without statistical significance. **The graph's contribution remains
measurable rather than measured.** See
[the results](../benchmarks/results/).

## Isolation

- **Process-level isolation between concurrent tasks needs bubblewrap**, which
  is usually unavailable inside a container. Without it, concurrent tasks share
  a PID view.
- **Landlock's network rules do not cover Multipath TCP**, and Go's `net.Listen`
  uses MPTCP by default. Port rules are augmented, not relied on alone.
- **Out-of-scope writes inside a worktree are caught by diff and policy, not by
  the sandbox.** The task legitimately has write access to what it is editing.

## Storage

- **SQLite needs a real filesystem.** Not an overlay layer, not a network share.
  `le doctor` fails if you put the data directory on one.
- **Downgrades across schema versions are not supported.** Migrations are
  forward-only; `le backup` before every upgrade.

## Language coverage

- **TypeScript is type-checked only with the sidecar.** Without Node the
  analyzer reads lexically and emits **no call graph at all**, because without a
  checker an identifier in call position may be a local or a shadowed binding.
  Every edge says which reading produced it.
- **Dynamic SQL is not resolved.** A query assembled at run time is recognised
  as a database call, but naming a table would be guessing.
- **Computed routes and URLs produce no edge.** A literal is required, because a
  guessed endpoint is worse than a missing one.

## Models

- **Reasoning models can spend an entire output budget thinking.** At 34 tok/s a
  single call can take four minutes. Truncation is reported as its own outcome
  rather than as the model deciding to stop, but the budget is yours to set.
- **A provider that misdeclares its capabilities breaks things at run time.**
  `le models conformance` checks; nothing forces you to run it.

## Deferred to a document that is not here

v3 defers three things to v2.0, which is not in this repository:

- the **1.0 acceptance matrix** (§1.2 refers to "the v2.0 Section 18 matrix");
- **Stage A, C and E** of the evaluation, named in §14 and §15 and defined
  nowhere in v3;
- the contents of **`workflows/`**, listed in the layout and described nowhere.

These are recorded rather than invented. `le models conformance` is built from
how v3 *uses* Stage A and is named for what it does.
