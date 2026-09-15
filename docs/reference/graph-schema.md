# Graph schema

## Evidence categories

Every edge records how the relationship was discovered. This is the most
important column in the schema: it is what lets a report distinguish "the
compiler told me" from "a heuristic guessed".

| Category | Meaning |
|---|---|
| `resolved` | A compiler, type checker or the filesystem established it |
| `declared` | A human or a manifest stated it (catalog, `ARCHITECTURE.md`, compose) |
| `inferred` | A heuristic derived it (literal route prefixes, dynamic SQL) |
| `observed` | Seen at runtime or in commit history |
| `unknown` | The analyzer could not classify it |

`resolved` and `declared` are *certain*; the rest are not. Impact analysis
reports uncertain consumers and treats them as present — it never filters them
out — but it declines to make a compatibility claim about them.

**A missing edge means "not discovered", never "does not exist."** Every impact
report carries that sentence.

## Node kinds

`file`, `directory`, `package`, `module`, `service`, `api`, `route`, `handler`,
`type`, `function`, `method`, `class`, `interface`, `field`, `variable`,
`constant`, `dependency`, `config_key`, `infra_resource`, `deployment`, `test`,
`build_target`, `schema`, `table`, `column`, `commit`, `doc`.

## Edge kinds

| Kind | Relationship |
|---|---|
| `contains` | Directory, file and package containment |
| `imports` | Import to dependency |
| `depends_on` | Module to module, package to package |
| `calls` | Caller to callee |
| `implements` | Interface to implementation |
| `uses_type` | Type to usage |
| `references` | API to consumer |
| `routes_to` | Route to handler |
| `handles` | Handler to service |
| `reads_config` | Configuration key to consumer |
| `writes_schema` / `reads_schema` | Migration or query to schema object |
| `tests` | Test to implementation |
| `builds` | Build target to dependency |
| `deploys` | Deployment component to service |
| `provisions` | Infrastructure resource to component |
| `touches` | Commit to file or symbol |
| `extends`, `embeds`, `returns`, `accepts` | Type relationships |

## Coverage and sources of truth

| Relationship | Source | Evidence | State |
|---|---|---|---|
| File, directory containment | Filesystem, git tree | `resolved` | **done** |
| Module and package graph | `go list` via `go/packages` | `resolved` | **Go** |
| Import to dependency | The compiler | `resolved` | **Go** |
| Caller to callee (static) | Type-checked call sites | `resolved` | **Go** |
| Caller to callee (interface dispatch) | CHA over `types.Implements` | `inferred` | **Go** |
| Interface to implementation | `types.Implements` | `resolved` | **Go** |
| Type to usage | `types.Info.Uses` | `resolved` | **Go** |
| Signature to type (accepts, returns) | The type checker | `resolved` | **Go** |
| Struct to field, embedding | The type checker | `resolved` | **Go** |
| Test to implementation | Call graph from `_test.go` | `resolved` | **Go** |
| Route to handler | Router-registration call sites | `inferred` | **Go** |
| Configuration to consumer | `os.Getenv`; compose, Dockerfile, Kubernetes, Terraform | `resolved` + `declared` + `inferred` | **done** |
| Schema definition | DDL in migrations | `resolved` | **done** |
| Schema to application code | Type-checked database call sites | `resolved` / `inferred` | **done** |
| Build target to dependency | Dockerfile `COPY`/`FROM`, Makefile targets | `resolved` + `inferred` | **done** |
| Deployment to service | Compose, Kubernetes manifests | `declared` | **done** |
| Infrastructure to component | Terraform HCL | `declared` + `inferred` | **done** |
| TypeScript: modules, imports, declarations, heritage, `process.env` | Lexical reading of the source | `resolved` / `declared` / `inferred` | **done** |
| TypeScript: call sites, resolved heritage, path aliases | TS compiler API, via the Node sidecar | `resolved` | **done, sidecar only**¹ |
| API to consumer | Route inventory plus literal client prefixes | `inferred` / `declared` | not yet |

¹ The sidecar (`sidecars/typescript/`) ships in the `cpu` and `cuda` images and
is used automatically when present. Without it — the `-slim` image, or a host
with no Node — TypeScript gets the lexical row only, and **no call graph at
all**: without a checker an identifier in call position may be a local or a
shadowed binding. `le index` says which reading it used.

The state column is deliberate. A schema that described relationships the code
does not produce would make impact reports look better than they are.

## The direction invariant

**Every edge points from the consumer to the thing consumed.** `A → B` means
"A depends on B, so a change to B may affect A".

This is not a convention about style. Impact analysis is a *reverse* traversal:
from the changed node it walks edges backwards to find what depends on it. An
edge pointing the wrong way is therefore invisible to impact analysis, and the
report comes back silently short rather than wrong in any way a reader could
notice.

Two edges were originally written backwards, and the consequences were exactly
that:

- `implements` pointed interface → type, so **adding a method to an interface
  reported zero implementations** — the most common breaking interface change,
  reported as compatible.
- `reads_config` pointed key → reader in the Go analyzer but reader → key in
  the deployment analyzers, so a configuration change found the manifests that
  set a variable but not the code that read it.

`internal/analyzers/direction_test.go` indexes a fixture spanning every
analyzer and asserts that a change to an interface, a config key and a table
each reaches its consumers. It exists so a third analyzer cannot reintroduce
the same class of silent incompleteness.

## Why interface dispatch is `inferred`

## Why interface dispatch is `inferred`

A call through an interface does not resolve to one function. The compiler
knows only the interface method; which implementation runs depends on the
value at runtime.

The analyzer over-approximates with class hierarchy analysis — every locally
declared type satisfying the interface is a candidate — and labels those edges
`inferred`, recording the assumption on the edge itself:

```json
{"dispatch":"interface","interface":"pkg.Loader",
 "assumption":"CHA: any locally declared type satisfying this interface may receive the call",
 "candidates":"2"}
```

Impact analysis then reports those consumers as `undetermined` rather than
making a compatibility claim it cannot support. That is the difference between
a tool that is useful and one that is confidently wrong.

Satisfaction itself comes from `types.Implements`, not from name matching: a
type with a `Load` method of a different signature is **not** reported as an
implementation, and there is a test that asserts exactly that.

## Why the schema parser is not `pg_query_go`

The design names `pg_query_go`, which embeds PostgreSQL's own parser through
cgo. That would give complete fidelity — and it would end the CGO-free build,
which is load-bearing: DR-1 ships one static binary, and the images are built
for amd64 and arm64 from a single runner.

What is here instead is a focused DDL parser covering the statements that
define a schema: `CREATE TABLE`, `ALTER TABLE`, `CREATE INDEX`, `CREATE VIEW`
and their `DROP`s, with migrations applied in file order so the result is the
schema as it ends up rather than a pile of statements.

What it cannot parse it **reports** as unparsed, with a count, rather than
skipping silently. If that fraction ever turns out to matter, `pg_query_go`
behind a build tag is the replacement path and the CGO-free default stays.

## Why some things produce nothing

A configuration key read as `os.Getenv(key)` where `key` is a variable
produces no edge. A route registered with a computed path produces no route. A
query assembled at runtime names no table. A `COPY *.go` in a Dockerfile
resolves to no file. A Helm template is recorded as present but not parsed,
because rendering it needs chart values the analyzer does not have.

All of these are real relationships the analyzer cannot name.

Naming them would mean guessing, and a wrong `config_key` node makes an impact
report confidently incomplete — worse than an obviously missing one. The
standing caveat covers it: a missing edge means "not discovered".

## Traversal

Recursive CTEs over the edge tables, bounded at `graph.MaxTraversalDepth` (6)
and capped at `graph.DefaultMaxNodes` (5000). Impact analysis traverses in
reverse to depth 3.

A path is only as strong as its weakest hop: the traversal carries the maximum
evidence rank along each path, and each node is reported once at its shortest
depth with the strongest evidence available at that depth.

When a traversal hits its node cap the report is marked `truncated` and
presented as a lower bound.

## Compatibility verdicts

`impact_of(changed_nodes, change_kind)` produces one of:

| Verdict | Meaning |
|---|---|
| `breaking` | The consumer cannot keep working unchanged |
| `compiles_behaviour_may_differ` | Still builds; needs a test, not an edit |
| `compatible` | No action expected |
| `undetermined` | The edge evidence is too weak to judge |

The mapping from (change kind, edge kind, evidence) to a verdict is a fixed
table in `internal/graph/impact.go`. No model is involved, so the same change
always produces the same report. Every `breaking` verdict carries the migration
step it requires; a fuzz test enforces that.

## Index keys

Each indexed unit records a content manifest, a lockfile hash, the toolchain,
the build mode and the indexer version. A change to any of them marks the unit
dirty, and dirty units are re-analysed before any step that needs the graph.
Bumping `version.IndexerVersion` invalidates every cached analysis.
