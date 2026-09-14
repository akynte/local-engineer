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

| Relationship | Source | Evidence | Implemented |
|---|---|---|---|
| File, directory containment | Filesystem, git tree | `resolved` | **yes** |
| Module and package graph | `go list`, `go mod graph`, TS program | `resolved` | not yet |
| Caller to callee | VTA/CHA call graph; TS checker | `resolved` | not yet |
| Interface to implementation | `types.Implements`; TS structural | `resolved` | not yet |
| Type to usage | `types.Info`; TS references | `resolved` | not yet |
| API to consumer | Route inventory plus literal client prefixes | `inferred` / `declared` | not yet |
| Route, handler, service, repository | Router registration plus call graph | `resolved` + `declared` | not yet |
| Schema to application code | `pg_query_go`, sqlc names, proto | `resolved` / `inferred` | not yet |
| Configuration to consumer | `os.Getenv`, viper, envconfig, compose | `resolved` + `declared` | not yet |
| Test to implementation | Call graph from `_test.go`, vitest | `resolved` | not yet |
| Build target to dependency | `go list -deps`, Dockerfile, Makefile | `resolved` + `inferred` | not yet |
| Deployment to service | Compose, Swarm, Helm, Kubernetes | `declared` | not yet |
| Infrastructure to component | Terraform HCL, manifests | `declared` + `inferred` | not yet |
| Commit to file and symbol | `git log -L`, blame | `observed` | not yet |

The "Implemented" column is deliberate. Today the graph holds the filesystem
and containment layer; language analyzers are the next phase. A schema that
described relationships the code does not produce would make the impact
reports look better than they are.

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
