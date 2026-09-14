# C4 level 3: components

Inside the supervisor. Every box is a package under `internal/`.

```mermaid
C4Component
  title Component view — internal/ packages and what they are allowed to touch

  Container_Boundary(sup, "le (supervisor)") {
    Component(api, "api", "HTTP", "Status, readiness, dashboard. Maps errors to codes and holds no business rule.")
    Component(taskpkg, "task", "Lifecycle", "Runs attempts, applies the completion contract, opens gates.")
    Component(engine, "engine/native", "Tool loop", "Nine tools, bounded steps, worktree-confined.")
    Component(retrieval, "retrieval", "Packets", "Lexical anchors, then graph expansion, then mandatory impact slots.")
    Component(graph, "graph", "Typed edges", "Traversal and impact analysis. Evidence category per edge.")
    Component(indexpkg, "index", "Indexer + watcher", "Walks repositories, runs analyzers, marks scopes dirty.")
    Component(analyzers, "analyzers/*", "Language + infra", "Go, TypeScript, SQL, proto/Avro, deploy, Terraform, git log.")
    Component(ledgerpkg, "ledger", "Journal", "Intent before the side effect. Recovery classifies by inspection.")
    Component(recipe, "recipe", "Verification", "Build, vet, test, race, format, semgrep — and the summarisers.")
    Component(worktree, "worktree", "Checkouts", "Per-task git worktrees, path confinement, out-of-scope detection.")
    Component(policy, "policy", "Repository rules", "What no task may change, independent of declared scope.")
    Component(broker, "broker", "Human gates", "Carries the impact report and diff to a person.")
    Component(llm, "llm", "Provider boundary", "Chat, structured, embed, infill, plus a Capabilities promise.")
    Component(sandbox, "sandbox/*", "Isolation", "Landlock and optional bubblewrap runners.")
    Component(store, "store", "Persistence", "The only package that opens a database or writes data-directory files.")
  }

  Rel(api, taskpkg, "status")
  Rel(taskpkg, engine, "one attempt")
  Rel(taskpkg, recipe, "verification")
  Rel(taskpkg, worktree, "checkout, diff, scope")
  Rel(taskpkg, policy, "protected paths")
  Rel(taskpkg, broker, "gate")
  Rel(taskpkg, ledgerpkg, "journal")
  Rel(engine, retrieval, "packet")
  Rel(engine, llm, "chat, tools")
  Rel(retrieval, graph, "expansion, impact")
  Rel(indexpkg, analyzers, "nodes and edges")
  Rel(indexpkg, graph, "writes")
  Rel(recipe, sandbox, "runs confined")
  Rel(graph, store, "queries")
  Rel(ledgerpkg, store, "queries")

  UpdateLayoutConfig($c4ShapeInRow="4", $c4BoundaryInRow="1")
```

## The rules this diagram encodes

**Everything persistent goes through `store`.** §2.3 requires it and a custom
`go/analysis` analyzer (`tools/analyzers/storescope`) enforces it: `sql.Open` and
file writes outside `internal/store` and `internal/artifacts` fail the build.
Four packages are exempt and each entry in the list must say why — the identity
pin, operator configuration, git checkouts, and memory notes are repository or
config files rather than workspace state.

**`api` holds no business rule.** A rule that only holds over HTTP does not hold
when the worker does the same thing. The handler maps domain errors to status
codes and nothing else.

**Edges point consumer → consumed, everywhere.** Impact analysis is a *reverse*
traversal, so an `implements` edge pointing interface → type would report zero
implementations when a method is added to an interface — exactly the question
the edge exists to answer. A cross-analyzer test holds Go and TypeScript to the
same invariant.

**The engine cannot reach outside its worktree.** Path confinement lives in
`internal/worktree`, not in the engine, because the paths come from the model
and confinement that lives next to the caller is confinement the caller can
forget.

## Reading order for a newcomer

1. `internal/store` — everything else is scoped by it.
2. `internal/ledger` — intent-first journalling, and what recovery does with it.
3. `internal/graph` — the direction invariant, and why evidence categories exist.
4. `internal/task` — the completion contract, which is where acceptance is decided.
