# Tool schemas

The tools the editing engine exposes to a model, and the JSON Schema each one
constrains its arguments with.

Schemas are strict — `required` fields, `additionalProperties: false` — because
a tool whose schema does not constrain its arguments produces malformed calls
from a small model far more often than from a large one. A malformed call costs
a whole step to discover.

## The surface is capped

The active profile's `max_tools_exposed` limits how many tools the model sees.
A small model given twenty tools picks the wrong one far more often than one
given eight, so the list below is ordered by importance and a cap drops the
least useful rather than an arbitrary subset.

**Only tools the engine can actually run are advertised.** An arm built without
retrieval is not offered `search_code`; one without a recipe runner is not
offered `run_verification`. Advertising a tool that always fails spends the
model's budget discovering that, and in an evaluation it charges the baseline
for the component it was meant to be measured without.

## Tools

| Tool | Needs | Arguments |
|---|---|---|
| `read_file` | — | `path` (required), `start_line`, `end_line` |
| `edit_file` | — | `path`, `old`, `new` (all required) |
| `write_file` | — | `path`, `content` (both required) |
| `list_files` | — | `dir` |
| `run_verification` | a recipe runner | `kind`: `build` \| `vet` \| `test` (required) |
| `search_code` | retrieval | `query` (required), `limit` |
| `find_symbol` | the graph | `name` (required) |
| `impact_of` | the graph | `symbol` (required), `change`: `signature` \| `behaviour` \| `remove` \| `rename` \| `add_field` |
| `git_touch` | the graph | `path` or `symbol`, `limit` |
| `done` | — | `summary` (required) |

## Notes on individual tools

**`edit_file` requires the exact existing text**, and the old string must appear
exactly once. This is deliberate: it cannot silently discard code the model
never read, which `write_file` can.

**`impact_of` is the reason the graph exists.** It reports consumers with their
evidence categories, a compatibility verdict, and the migration steps a change
implies. Consumers with `inferred` or `unknown` evidence are treated as present,
because a missing edge means "not discovered" rather than "not there".

**`git_touch` reads indexed history, not git.** It answers from the
commit-to-file edges the gitlog analyzer wrote. Shelling out to git would hand a
model a general-purpose command inside the checkout it is editing, and the index
is already scoped to this workspace. Its edges are `observed`: a commit touching
a file is a fact about history, not about whether the code is related today.

**`done` is an input, not a decision.** The supervisor checks the evidence
itself; calling `done` before verification passes wastes an attempt.

## Confinement

Every path argument is resolved inside the task's worktree by
`internal/worktree`, not by the engine. The paths come from the model, and
confinement that lives next to the caller is confinement the caller can forget.
Traversal and symlink escapes are tested.
