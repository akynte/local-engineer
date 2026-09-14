# Trust boundaries

Who can affect what, and what each boundary does **not** protect against.

## The boundaries

```
  you  ──────────────────────────────────────────────────────────────
   │                                                    trusted
   │  gates: you approve a diff before it is applied
   ▼
  supervisor  ─────────────────────────────────────────────────────
   │                                                    trusted
   │  owns the ledger, the policy, the acceptance decision
   ▼
  task sandbox  ───────────────────────────────────────────────────
   │                                                    UNTRUSTED
   │  runs model-chosen commands over your code
   ▼
  model output  ───────────────────────────────────────────────────
                                                        UNTRUSTED
```

## What each boundary is for

### You and the supervisor

The supervisor never applies a change without a gate, when gates are on. §3.3
puts the impact report and the diff in front of you, and `le doctor` reports
when gates are off — a system running without them should never be a surprise.

**Does not protect against:** you approving something you did not read. The gate
carries deterministic evidence rather than the model's account of its own work
precisely so that reading it is worthwhile.

### The supervisor and the task

Everything the model does is a tool call the supervisor executes. Paths are
resolved inside the worktree by `internal/worktree`, not by the engine, because
the paths come from the model. Traversal and symlink escapes are tested.

The task edits a **per-task worktree**, never your checkout. An accepted change
is applied afterwards, through the gate. A crash therefore cannot leave your
files half-edited.

**Does not protect against:** a change that is legal, compiles, passes every
test, and is still wrong. That is what the completion contract and the hidden
acceptance tests in the evaluation are for — and the evaluation reports false
acceptance as its own number because it is the failure that matters most.

### The sandbox

Three layers, and `le doctor` reports which are active (§6.2):

| Guarantee | Container | + Landlock | + bubblewrap |
|---|---|---|---|
| Cannot touch host files outside mounts | yes | yes | yes |
| Cannot read another workspace's data | by permissions | yes | yes |
| Cannot reach model-management endpoints | proxy allowlist | yes | yes |
| Cannot see other tasks' processes | no | no | yes |
| Cannot modify policy, ledger, hidden tests | permissions | yes | yes |

**Does not protect against:** out-of-scope writes *inside* the worktree. The
task legitimately has write access to the checkout it is editing, so a change to
a file it should not have touched is caught by diff and by policy, not by the
sandbox. That is why `policies/` exists.

## The one that is easy to miss

**Model output is data, never instruction.** A tool result, a file the model
read, a commit message in the repository — none of it is a directive to the
supervisor. The engine parses tool calls against a schema and validates
arguments; it does not act on prose.

This matters because the repository being worked on is not necessarily yours.
Indexing someone else's code means reading text they wrote, and text that tries
to instruct an agent is a real thing. The deterministic layer answers structural
questions from the index; the model never gets to decide what the supervisor
does with them.

## What is deliberately trusted

The **model itself**, in the sense that a confused model can waste a budget. It
cannot escape the sandbox or apply a change without a gate, but it can spend
your time. That is a cost, not a vulnerability, and the budgets exist to bound
it.

The **operator's configuration**. `providers.yaml`, the profile and the policy
are yours; nothing validates them against an external authority. `le models
conformance` checks a provider does what it claims, which is the one place a
configuration mistake would otherwise surface as the model seeming bad at its
job.
