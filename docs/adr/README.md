# Decision records

Every architectural decision is recorded here in the seven-point format design
v3 §12 requires, laid out in [MADR](https://adr.github.io/madr/) style:

1. **Problem** — what had to be decided
2. **Alternatives** — what else was on the table
3. **Evidence** — what the decision rests on
4. **Chosen** — what was decided
5. **Why** — the reasoning
6. **Disadvantages** — what this costs. *Not optional.* A record with no
   disadvantages has not been thought through.
7. **Replacement path** — how to undo it later. A record without one is a trap
   for whoever maintains this next.

Records are never edited to make a past decision look better. When the evidence
changes, a new record supersedes the old one and says what changed.

| | Decision | Status |
|---|---|---|
| [DR-1](0001-single-container-sqlite.md) | Single-container distribution with SQLite storage | Accepted |
| [DR-2](0002-graph-in-sqlite.md) | Graph in SQLite edge tables rather than an embedded graph database | Accepted |
| [DR-3](0003-layered-sandbox.md) | Container boundary plus Landlock, bubblewrap optional | Accepted |
| [DR-4](0004-openai-compatible-provider.md) | OpenAI-compatible HTTP as the provider boundary | Accepted |
| [DR-5](0005-opencode-engine.md) | OpenCode as the execution engine | Superseded by DR-7 |
| [DR-6](0006-workspace-identity.md) | Workspace identity as the isolation key | Accepted |
| [DR-7](0007-native-engine.md) | A native engine on the provider boundary | Accepted |
| [DR-8](0008-tree-sitter-and-cgo.md) | tree-sitter for language-agnostic structure, ending the CGO-free build | Accepted |
