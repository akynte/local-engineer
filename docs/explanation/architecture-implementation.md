# Architecture implementation status

The [architecture review](../../local-coding-system-review.md) is authoritative.
The [gap assessment](../../local-coding-system-gap-assessment.md) is the initial
assessment at revision `9e07fe1`; its findings describe the state before this
migration. This page tracks implementation, without changing the architecture.

## First increment: native editor boundaries

Implemented:

- Native file writes require an explicit task scope. An empty scope grants no
  writes; `le task create --scope` and materialized plan scopes supply it.
  Exact paths, directory prefixes, globs, and a trailing `/**` are supported.
- The native dispatcher validates required arguments, types, enums and unknown
  fields against its advertised tool schemas. Unadvertised tools cannot execute.
- File access checks resolve symlinks, including ancestors of new files, before
  checking scope. Secret paths are denied for reads and writes. Writes to
  supervisor metadata, generated files, and operator-protected paths are denied.
- Sensitive paths are excluded from new indexes and filtered from retrieval
  packets, including packets built from an older index. This is a path policy,
  not a content classifier for secrets copied into ordinary source files.
- Native tool allow/deny decisions are journalled before execution and results
  afterwards. A journal write failure stops the attempt.
- The native EDIT transcript is append-only for the attempt. Context admission
  includes tool schemas and reserved output. It stops before an estimated
  overflow instead of dropping earlier exchanges. The task becomes `blocked`,
  with the boundary reason checkpointed; it cannot be accepted as complete.
- Task token limits are passed as the remaining allowance on each attempt and
  checked before model requests. Token counts used for admission are estimates;
  reported provider usage is used for accounting, with an estimate when the
  provider omits usage. The phased native runner persists these counters across
  resume; the legacy runner still counts within one invocation.

This increment applies to the native editor. Command sandboxing remains
separate from these checks. The OpenCode boundary and the regeneration phase
are the third increment below.

## Persisted native workflow

The native task runner now uses INTAKE → LOCALIZE → IMPACT → PLAN → EDIT →
VERIFY → REVIEW → FINALIZE, with validated transitions persisted in the ledger
database. Localization uses three structured calls: repository paths, selected
file signatures, then bounded source bodies. PLAN validates exact file grants
against operator scope before EDIT can write. Existing graph impact is supplied
to planning and review.

EDIT persists the complete append-only transcript, fence token, tool counters,
candidate, and completion marker. Recovery refuses an interrupted tool batch or
an externally changed candidate. Context exhaustion permits at most two explicit
replanning boundaries. The runner persists task token and wall-clock accounting.

Verification captures a baseline, fingerprints failures, records repeated and
regression failures, and checks unchanged reruns for flakiness. Reruns consume
the verification-loop budget. Environmental failures block the task. Exhausted
repair attempts restore the isolated task checkout to its original base.
Fresh-context structured review must accept before final approval and commit;
rejection routes to bounded repair. Finalization scans added diff lines for
private keys and recognizable AWS/GitHub credentials without logging their
contents. This does not detect arbitrary secret formats. REVIEW defaults to the
editing provider and uses an explicitly configured review provider when present.

Fixture tests cover review acceptance/rejection, restart prefix preservation,
interrupted tool batches, fingerprints, and forbidden phase transitions.

## Second increment: structure, obligations, budgets, memory

Implemented since the first increment:

- Interrupted native tool batches reconcile from the journal instead of
  blocking. Recorded results are restored; a remainder that never started is
  closed with an explicit boundary. A possibly completed side effect is never
  re-executed, and a worktree whose manifest no longer matches still blocks.
- Impact is connected to explicit caller obligations. The plan schema carries
  an obligation per affected consumer with a required resolution — an edit, or
  "no change needed" with a concrete compatibility reason — and the plan is
  rejected while any discovered obligation is unresolved. Obligations are
  recomputed from the actual diff at VERIFY: an exported signature the diff
  changed re-derives its consumers, and an unplanned consumer routes back to
  planning within the same two-replan budget.
- Compiler-produced SCIP indexes import into the workspace index (`le index`),
  which gives exact occurrences and relationships for any language with a SCIP
  indexer, Rust and TypeScript included. There is no live LSP client yet, so
  cross references are as fresh as the last index.
- A ranked repository map (declarations scored by incoming references, rendered
  as signatures only, on its own token budget) and a signature-only skeleton
  pass feed LOCALIZE, ahead of any source bodies.
- Regression classification uses individual test outcomes. The Go test parser
  keeps per-test results, so a newly broken test is a regression even when the
  baseline already had a different failure.
- Verification presets are discovered per module across a monorepo — Go, Cargo
  and Node workspaces, with nested workspace commands removed — and every frozen
  preset must pass on the verified candidate, not one preset per kind.
- Project memory cards in `.agent/` load with provenance and staleness checks:
  a mismatched repository, an unreviewed source, or a symbol that no longer
  resolves in the live graph marks the card stale, and stale cards are dropped
  from the packet rather than quietly trusted. FINALIZE writes the task card.
- Per-phase context, output and reasoning budgets are profile configuration
  with defaults from review §7.2, and a managed llama.cpp process gives one-slot
  profile swapping: the owned process is unloaded before the next is loaded, and
  an externally started server is never killed.
- Model-visible routes outside the diff are checked at the route. Graph impact
  reports drop protected paths (and mark the report truncated), retrieval
  filters sensitive paths again at the boundary, and the memory-note and
  recorded-answer tools refuse content matching a credential pattern — those
  two persist outside the worktree, so the finalization gate never sees them.

## Third increment: the OpenCode boundary and regeneration

`le opencode run` starts the editing session instead of leaving the developer
to start it, which is what makes the boundary enforceable: until now the tools
were reachable from a session nothing confined.

- The session runs under the strongest layer `le doctor` reports. It may write
  its worktree, its tmp and this workspace's OpenCode state, and read the
  operator's toolchain paths, the interpreter's own install tree, and the
  `/proc` and `/sys` entries a managed runtime reads before it runs any of the
  program's code. When no layer is available the command refuses and says why;
  `--unconfined` is the explicit opt-out.
- The environment is built from nothing rather than filtered, because the
  variables that matter — an agent socket, a session token a developer sourced
  an hour ago — are not files and no path rule hides them. XDG directories point
  at this workspace, so one workspace's session never reads another's history
  or credentials.
- A restricted `le-editor` agent is merged into the repository's
  `opencode.json` on every run, refusing the shell, web-fetch, web-search,
  subagent and external-directory tools. This is the convenience layer, not the
  boundary: the shell's own enforcement has documented bypasses, which is why
  the sandbox and the supervisor's checks are what the design relies on.
- A kernel test runs the real Landlock ruleset built from a session's own spec
  and confirms it reads the worktree and is denied a credential outside it.

Generated files now have the declared regeneration the first increment left
open. A plan that grants writes to a generated path is refused and told to
declare its generator instead; the generator must be a preset the operator
froze during INTAKE whose kind is generation. Generators run before the checks
that read their output, and a generated file a declared generator rewrote is no
longer reported as a change outside the plan's allowlist. What lands is what
the generator produced, not what a model believed it would produce.

## Fourth increment: the remaining §4 boxes

The review's §4 diagram is the target. This increment built the boxes that were
missing or substituted, and the reconciliation below says where the code still
differs from the drawing.

**Context packer.** Every model request is now a frozen prefix, an append-only
log and a small tail. P0 is a constant operating policy, P1 the repository card,
P2 the ranked map, P3 the task card; the log is appended to and never reordered;
the tail carries the phase instruction and the budget note. The frozen region is
built once and persisted rather than recomputed, and the request declares where
it ends so a cache-aware provider keeps the layout.

The bug this found is worth recording: the three structured phases minted a new
fence token per call, and the token is part of the policy preamble, so the first
message differed on every request and every call was a full prefill. That is the
exact cost §7 is written to avoid, in the code written to follow it. There is
now one fence per task, and a test asserts the prefix is byte-identical across a
task's calls rather than merely similar.

A full log is `ErrLogFull` — a phase boundary — not a silent drop of the oldest
entry. Compaction rebuilds §20's record from the ledger: facts a tool
established, changes on disk, fingerprints still open. It never summarises the
conversation.

**ripgrep.** Live search over the working tree with a 200 ms budget and result
caps, patterns passed as fixed strings so task text cannot become a regular
expression, secret and vendored paths dropped. A truncated search says so: "no
other callers" and "no other callers in the first 200 ms" are different answers.
Task text expands into the spellings source actually uses — `accountLimit`,
`account_limit` — and the structure pass now sees the files that matched rather
than the whole tree.

**Live LSP client.** Written rather than depended on, as §16 says: initialize,
text synchronisation, definition, references, implementation, document symbols,
diagnostics. It is consulted only for files an attempt has already changed,
which is §11's rule and the one place SCIP is necessarily wrong — the index was
built before the edit, so it cannot know who calls a symbol the edit introduced,
and an obligations check reading only the index would pass by finding nothing.
Servers are operator configuration, started lazily, and a missing binary or a
server still indexing leaves the index's answer standing.

**Network isolation.** `sandbox.Spec.Network` defaults to no network, so a
caller that says nothing gets §9's default rather than the host's network. The
bubblewrap layer unshares the namespace; loopback survives, so tests that bind
127.0.0.1:0 are unaffected. The confined editing session is the one child that
asks for egress, because it has to reach the model gateway. The guarantee table
gained a row, and only bubblewrap claims it.

**Trace export.** `le trace <task>` prints the chain §21 asks for — what each
operation intended before the side effect and what it recorded after — and
`--jsonl` is §18's export: one object per line, so a trace cut off by a killed
pipe is still parseable up to the cut.

**Tools proxied to the firewall.** `le_read` and `le_edit` apply the same
`firewall.Access` the native editor uses, and the confined session runs with
OpenCode's own read and edit denied so they are the only route. The sandbox
bounds the session to the worktree; what it cannot express is the rest of §9's
path policy, and a committed `.env`, a generated file and every path the task
never declared are all inside the worktree. `le_task_start` now takes the write
scope, so the allowlist is declared before the work rather than discovered from
the diff.

## Where the code still differs from the §4 diagram

- **Tree-sitter** is now present, and it cost the static binary:
  [DR-8](../adr/0008-tree-sitter-and-cgo.md) records the decision and what it
  buys. Go, Rust and TypeScript/TSX grammars are compiled in; Vue is not,
  because the published grammar has no Go module, and a `.vue` file is reported
  as unexaminable rather than as clean. It answers two questions and no others —
  which declaration encloses a line, and which signatures changed between two
  versions of a file — with resolution staying where the compilers are.

  The gap that justified it was the silent one. Signature obligations read the
  changed files and skipped everything that was not `.go`, with no error and no
  note, so an attempt could change a public Rust function and the check would
  find no callers to account for because it never looked. It now runs for every
  language with a grammar, and names the changed files it could not examine.
- **The EDIT box.** The diagram makes the agentic phase an OpenCode session.
  The phased runner uses the native editor — §23 step 5's built-in — and
  `le opencode run` is the confined OpenCode path beside it. §23 step 7 says to
  compare the two in the harness and keep whichever wins; that comparison is one
  of the measurements below, so both are kept and neither is claimed better.
- **Embeddings and the reranker** are absent, which the diagram marks optional
  and §6.5 says to measure before adding.

## Remaining work

1. Measure what the review asks to be measured: the inference-depth, cache and
   MTP matrix per profile, and the §22 component ladder over historical tasks
   with a held-out comparison. This needs the target machine and the models.
   Nothing here claims a quality or a speed result — including the context
   packer, whose whole justification is a cache-hit rate no test here observes.
2. The external documentation tool with its domain allowlist (§23 step 12).

Passing unit tests does not demonstrate cache hits, model quality, or full
compliance. Those claims require the measurements and end-to-end tasks specified
in the architecture review.
