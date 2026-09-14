# Crash recovery

A long-running task on a laptop will be interrupted. The machine sleeps, the
model falls over, the container is updated, someone hits Ctrl-C. The design
treats interruption as normal rather than exceptional.

## The problem with "save your progress"

The obvious approach — periodically write down what has been done — has a hole
exactly where it matters. If the process dies *during* a side effect, the
record says either "about to do X" or "did X", and neither is true. The file
may be written, half-written, or untouched.

Asking the model to summarise what it did is worse: it loses facts, and it
cannot know whether a write landed.

## Intent first, outcome second

Every model-visible action is journalled as an *operation*:

```sql
CREATE TABLE operations (
  id INTEGER PRIMARY KEY, task_id TEXT NOT NULL, seq INTEGER NOT NULL,
  kind TEXT NOT NULL,
  intent JSON NOT NULL,     -- written BEFORE the side effect
  outcome JSON,             -- written AFTER; NULL means uncertain
  candidate_before TEXT, candidate_after TEXT,
  evidence_id TEXT, started_at INTEGER, finished_at INTEGER,
  UNIQUE (task_id, seq)
);
```

The ordering is the whole mechanism. The intent row is committed before the
side effect starts, so a crash always leaves one of three states:

| Journal | Meaning |
|---|---|
| No row | The action never started. Nothing happened. |
| Intent, no outcome | **Uncertain.** It may have happened, partly happened, or not. |
| Intent and outcome | It happened, and the outcome says what. |

The ledger runs with `synchronous=FULL` and write transactions take the lock
immediately, because this file is the recovery record and must survive a hard
kill.

## Resolving uncertainty by looking

An uncertain operation is not guessed at. Recovery inspects the world.

An edit's intent declares the path, the hash before, and the hash the file
should have after. On recovery the file is hashed:

| The file hashes to | Verdict |
|---|---|
| the after-hash | **complete** — the write landed |
| the before-hash | **not applied** — it never started |
| neither | **partial** — something is half-written |

Read-only operations — a search, a retrieval, a file inspection — are marked
*unknown* and are safe to repeat, because repeating them changes nothing.

A **partial** verdict makes the task unsafe to resume automatically. That is
deliberate: replaying an edit whose outcome is uncertain is how a half-applied
change becomes a corrupted one.

## Reconstructing the working state

Recovery rebuilds what the task knew, from the last checkpoint plus the
completed operations:

- the objective,
- which files and symbols were investigated,
- accepted decisions,
- **rejected hypotheses with the evidence that ruled them out**, so a resumed
  session does not spend its budget re-testing a dead end,
- completed edits,
- validations already run, and **which of them are stale**,
- the remaining plan, and the next action.

## Stale evidence

Evidence is recorded against the *candidate* it was produced for — a hash of
the whole worktree's contents. When the worktree moves on, that evidence no
longer applies:

```console
$ le task recover
  validations: 3, 1 stale
  next:        re-run stale validations against the current candidate
```

"The tests passed" is only a fact about a particular state of the code. Without
this, a resumed task would happily believe a green result that described
something that no longer exists.

## Leases

A worktree is claimed by a lease with an expiry. A live lease held by someone
else cannot be taken. A crashed holder's lease expires, and a new instance
takes it *after* reconciling.

Two instances never write the same worktree. That is the one property that
makes recovery safe to run automatically on every start.

## The interruption classes

All of these produce the same journal state, and are handled the same way:

| Interruption | What the journal shows |
|---|---|
| Application restart | Intent with no outcome |
| Model failure | Intent with no outcome, plus a recorded error if it was caught |
| User interruption (Ctrl-C) | Intent with no outcome |
| System crash / `kill -9` | Intent with no outcome |
| Process termination | Intent with no outcome |
| Hardware failure | Intent with no outcome |
| Timeout | Intent with no outcome |
| Context restart | Checkpoint plus handoff, no uncertainty |

The uniformity is the point. There is no separate code path per failure mode,
so there is no rarely-exercised path to get wrong.

## Seeing it

```console
$ le task list
$ le task journal <task-id>
SEQ  KIND          OUTCOME    EVIDENCE
1    session_start recorded
2    retrieval     recorded   ev-4a1
3    edit          UNCERTAIN

$ le task recover
```

## What this does not do

It does not undo a completed edit — git does that, and the worktree is a git
worktree.

It does not make a partially-applied change safe. It tells you there is one and
refuses to resume automatically, which is the most a mechanism at this level
honestly can.
