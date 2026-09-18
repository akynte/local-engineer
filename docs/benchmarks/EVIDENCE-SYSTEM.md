# The evidence system

This describes the machinery built to find out whether Local Engineer helps.
It is not a claim that it does. At the time of writing the qualification gates
fail, the README claims no benefit, and this document exists to say precisely
what would have to be true for that to change.

## 1. What was already there

The repository already had an evaluation harness, and the work started by
reading it rather than by writing anything. `internal/eval` had arms, a runner
that copies a fixture, lets a solver work, then copies hidden acceptance files
in and runs them, a McNemar exact test, a bootstrap over per-pair grade deltas,
Wilson intervals, and a grading pass. `evals/tasks` had ten real tasks. Two
pilot result files sat in `docs/benchmarks/results/`.

The important property was already correct: the acceptance command is not in
the worktree while the task runs, and the system's own claim is recorded in a
separate field from the ground truth. Nothing here needed to be rebuilt. The
gaps were in what the harness did not record, what it did not compare, and what
it did not refuse to conclude.

## 2. What the model's claim is worth

Nothing, as evidence of success. `Outcome.Solved` is set only by the hidden
acceptance command. `Outcome.Claimed` is a separate field, and the only place
the two meet is `FalseAccept = Claimed && !Solved` — which is a failure metric,
not a success one. No code path lets a claim raise a success rate.

False acceptance is reported as a first-class number per arm, with its own
confusion matrix (`internal/eval/acceptance.go`): a claim that was right, a
claim that was wrong, a silence that was right, a silence that was wrong. It is
the metric this project should be judged on most harshly, because a tool that
confidently reports finished work that is broken is worse than one that reports
nothing.

## 3. Runs that are not evidence

A run can end in six ways (`internal/eval/schema.go`): completed, task failed,
environment failed, timeout, invalid, cancelled. Only the first two are
evidence. The other four say something went wrong with the measurement, not
with the task, and folding them into a denominator either flatters or punishes
an arm for reasons that have nothing to do with it.

They are not discarded. Every run is written to its own file under `--raw`
before anything is aggregated, including the ones that crashed, and the
per-arm status counts appear in the generated report. A harness that keeps only
the runs it liked cannot be audited, and the selection is invisible in the
summary it produces.

## 4. Repeated runs are not independent samples

Running one task five times gives five numbers, not five tasks' worth of
evidence. A Wilson interval over fifty runs of ten tasks is narrower than the
truth, because it assumes fifty independent draws when there are ten clusters.

`internal/eval/hierarchical.go` resamples in the shape the data actually has:
tasks with replacement first, then runs within each drawn task. The interval it
produces is wider, and it is the one the report leads with. The Wilson
intervals are still shown, with a sentence saying exactly why they are too
narrow, because removing them would hide the discrepancy rather than explain it.

## 5. Overlapping intervals are not proof of no difference

Four verdicts, not two (`Verdict` in `hierarchical.go`): positive evidence,
negative evidence, no material difference, insufficient evidence. The last two
are different findings and the code keeps them apart. An interval that spans
[-0.16, +0.20] does not say a component does nothing; it says the measurement
cannot tell. Only an interval that both contains zero and is narrow enough to
exclude an effect worth caring about earns `no_material_difference`.

On the current pilot data every comparison returns `insufficient_evidence`.
That is the honest reading of ten tasks.

## 6. The component ladder

Each rung adds one component to the rung below, and the delta between adjacent
rungs is what that component is worth.

Four rungs are wired today, because four is how many toggles genuinely change
what executes: the unsupervised baseline, the supervised scaffolding with
lexical retrieval, the verification loop, and the reference graph. A fifth arm,
`supervised-no-graph-no-verify`, was added because the ladder needed it and the
combination was real.

Five further rungs are named and marked **not wired**, each with the reason:
fresh-context review, the frozen-prefix context architecture, project memory,
embedding retrieval and reranking. The first three are not measurable by this
harness because it attaches to the native engine, and those components live in
the phased runner above that attachment point. The last two do not exist.

Naming them keeps the gap visible. Omitting them would have made the ladder
look complete, and inventing arms to fill them would have been worse than
either.

A test asserts that adjacent wired rungs differ by exactly one toggle. Without
it, a delta could silently start carrying two components and the attribution
would be false while still looking like a measurement.

## 7. A ladder with a hole is not a ladder

The qualification gate originally counted ladder arms present. Three arms
passed it. But the three present were A, C and D: rung B had no runs, so the
A→C delta contained two components at once and could not be assigned to either.

The gate now counts the longest unbroken chain, which is 2. It fails. This is
the one place where tightening the code turned a passing gate into a failing
one, and it is the change that most improves the system's honesty.

## 8. The qualification gates

`le eval qualify` reads artefacts and reports whether anyone is entitled to an
opinion yet. Twelve gates: thirty held-out real tasks, repeats on the finalists,
hidden verification on every task, a baseline comparison, three consecutive
ladder rungs, a graph ablation, a context ablation, false acceptance measured,
paired statistics, raw runs retained, complete environment metadata, and an
independent reproduction that agrees.

Against the real committed data it reports FAIL on five gates. That output is
the deliverable. It says what the next piece of work is:

- **heldout_real_tasks: 0 of 30.** Every current task is in the dev set. Below
  thirty, one task flipping moves the headline more than any effect worth
  claiming.
- **component_ladder: chain of 2.** Rung B has never been run.
- **context_ablation: not computed.** Prefix reuse verified against a server is
  not the same as time saved across a task.
- **environment_metadata: missing model and runtime version.** The pilot files
  predate environment capture.
- **clean_reproduction: not run.** A headline resting on one execution rests on
  one roll of the dice.

## 9. Reproducibility

`internal/eval/environment.go` captures the commit and whether the tree was
dirty, the Go version, the model file and its SHA-256, quantisation, runtime
name, version and arguments, context length, KV cache types, sampling
parameters, seed, GPU, VRAM, CPU, cores, RAM, OS and kernel. Probing is
best-effort; a field that could not be read is rendered as *not recorded*
rather than omitted, because an absent row reads as "did not apply" and an
explicit gap reads as "nobody wrote this down".

The `environment_metadata` gate requires the fields without which a number
cannot be compared to another number at all.

## 10. Nothing in the results was typed by a person

`le eval results` generates `summary.json` and `RESULTS.md`. Every figure is
computed from the runs; the document carries a header saying not to edit it. A
table a human transcribed is a table that can drift from the run behind it,
usually in the flattering direction, and checking it is work that does not get
done.

The committed `RESULTS.md` is generated from the pilot files and carries a
provenance banner above every number saying so.

## 11. What was not built, and why

- **Embedding retrieval and a reranker.** The architecture says to measure
  before adding them. They are ladder rungs H and I, marked unwired.
- **Arms for review, frozen prefix and memory.** These would require the
  harness to attach to the phased runner rather than the native engine. That
  is real work, not a flag, and claiming the arms exist would have been the
  dishonest option.
- **A second CLI.** Everything is under the existing `le eval`.
- **Task generation.** No task in the set was invented to make a number move.

## 12. Verification of the machinery itself

`golden_test.go` fixes a hand-computed dataset and asserts the statistics
against numbers worked out by hand: false acceptance rates, a paired clustered
delta, determinism under a seed, seed-sensitivity at low resample counts,
convergence at high ones, exclusion of invalid runs from denominators, and
backward compatibility with result rows written before `status` existed.

`ladder_test.go` asserts that every wired rung names a real arm, that adjacent
rungs differ by one toggle, that unwired rungs cannot satisfy the gate, that a
gap breaks the chain, that a rung with only failed runs cannot fill a gap, and
that retention distinguishes no effect from no evidence.

## 13. The honest summary

The evidence system works. The evidence does not yet exist.

What can be said today is narrow and true: on ten dev-set tasks, under an
earlier model and configuration, no measured difference between any pair of
arms is distinguishable from noise, and the supervised arm produced false
acceptances in about a fifth of the runs where it claimed success. Nothing in
that supports a claim that the system helps, and nothing in it supports a claim
that it does not.

The README's evidence section says that and no more. The path to being able to
say something stronger is the five failing gates above, in that order, and the
command that will decide it is the same one that reports the failure now.
