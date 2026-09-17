# Pre-registration: does the code graph earn its cost?

Written and committed **before** the run it describes, and not edited afterwards.
A later commit may add the result beneath it; it may not change what is above.

## Why this exists

The first measured run of the large-fixture task set reported the graph adding
nothing and costing 1.8× the tokens. Investigating that found a defect rather
than a finding — expansion had no budget and took whatever the anchors left, and
a relevance score was computed for every slice and never read
([074bba8](../../commit/074bba8)). Both are fixed.

That creates an obvious hazard. The people re-running a measurement after fixing
something are the people who want the fix to have worked, and "the graph does
help, once you squint at the right subset" is a conclusion this evaluation could
reach by accident. Writing the criteria down first is what makes the answer
falsifiable rather than negotiable.

## The question

Does graph expansion and impact analysis improve task success beyond lexical
retrieval, on tasks whose consumers live in a different package from the change?

That qualifier is the claim being tested, not a convenient slice. The graph's
stated case in the design is impact analysis across package boundaries: a
signature change whose callers the compiler finds only after the change is made.
A task set without such work does not test it.

## What is fixed in advance

**Arms.** `supervised` against `supervised-no-graph`, which differ structurally:
graph expansion and impact analysis off, lexical retrieval left on.

**Primary outcome.** Paired McNemar exact test on solved, over the runs both arms
attempted. Two-sided, α = 0.05.

**Secondary outcome.** Paired bootstrap on the graded score — the share of each
task's hidden tests satisfied — 95% interval over per-pair differences.

**Repetitions.** 5 passes. The cell instability seen before (9 of 20 changing
verdict) is the reason this is not 1.

**Task set.** Every task on the 2,075-line `warehouse` fixture. Tasks are not
selected after seeing results, and none is added or removed once this run starts.

## What counts as what

**The graph earns its cost** if the primary test is significant in its favour, or
the secondary interval excludes zero in its favour, *and* the token cost is
reported beside the result rather than omitted.

**The graph does not earn its cost on this set** if neither is true. That is a
publishable outcome and the honest one if it happens. It would not prove the
graph worthless — it would say this set, at this size, on this model, cannot
detect a difference, and the roadmap entry stays open.

**The result is void** if the run is interrupted, if the arms hit the wall clock
at materially different rates, or if any task is changed mid-run.

## Confounds being removed first, and why

The previous run is not a clean comparison of capability. Two reasons, both
measured rather than suspected:

- **Time bound the fuller pipeline first.** `supervised` hit a ceiling on 8 of 25
  runs against `supervised-no-graph`'s 5. A fixed wall clock scores an arm partly
  on being slower, and the graph arm was slower largely because of the budget
  defect now fixed.
- **Two ceilings, not one.** Eleven runs ended at exactly 600s — the provider's
  request timeout, not the task budget — and eight at 900s.

Both budgets are raised for this run so that capability, not the clock, is what
separates the arms. The rates are reported; if they are still materially
different the result is void by the rule above.

## What will be reported either way

The arms table with intervals, the paired test and its discordant counts, the
graded difference and its interval, median tokens and wall clock per arm,
ceiling-hit counts per arm, and cell instability across passes.

A result that does not separate the arms will be published in the same place and
with the same prominence as one that does.
