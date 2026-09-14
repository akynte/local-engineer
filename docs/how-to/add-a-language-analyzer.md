# Add a language analyzer

An analyzer turns files into typed nodes and evidence-tagged edges.

## The interface

```go
type Analyzer interface {
    Name() string                    // stored as the edge source, so a wrong edge is traceable
    Handles(f index.File) bool       // which files this wants
    Analyze(ctx, repoRoot string, files []index.File) (Result, error)
}
```

Register it in `analyzers()` in `cmd/le/index.go`. The indexer runs every
analyzer, writes all nodes, and only then resolves edges — so an analyzer never
has to care what order it ran in.

## The two decisions that matter

### Which evidence category

§3.2 records a category for every relationship, and "unknown" is a valid,
honest answer. Choose by asking what would make the edge wrong:

| Category | Means | Example |
|---|---|---|
| `resolved` | a compiler, type checker or filesystem fact | `types.Implements`; an import that resolves to a file on disk |
| `declared` | a human or a manifest states it | a compose service; an `extends` clause |
| `inferred` | a heuristic | a literal route prefix; a `process.env` read |
| `observed` | seen at runtime or in history | a commit touching a symbol |
| `unknown` | the analyzer could not classify it | — |

Getting this wrong is not a cosmetic error. `impact_of` treats `inferred` and
`unknown` consumers as present, and a `resolved` edge that is actually a guess
makes an impact report confidently incomplete.

The TypeScript analyzer is the worked example: it reads lexically and therefore
emits **no call graph at all**, because without a type checker an identifier in
call position may be a local, a shadowed binding, or a method on an unrelated
object. An edge that is wrong half the time is worse than no edge. The sidecar,
which has the compiler, emits those edges as `resolved`.

### Which direction

**Every edge points consumer → consumed.** Impact analysis is a *reverse*
traversal, so an `implements` edge pointing interface → type would report zero
implementations when a method is added to an interface — precisely the question
the edge exists to answer.

`internal/analyzers/direction_test.go` holds every analyzer to this with one
fixture. Add your analyzer to it; a new language getting the direction wrong is
the single most likely mistake, and it is silent.

## Reporting what you could not do

Take a `Warnf` and use it. A file that failed to parse, a construct you skipped,
an ambiguous name you refused to guess at — say so. A partial graph that reports
its gaps is useful; a partial graph that looks complete is not.

```go
if len(candidates) != 1 {
    // Two exported types with the same name in different modules is common.
    // Picking one is wrong half the time, so pick neither.
    return "", "", false
}
```

## Testing it

Run the analyzer against a real fixture on disk, not a mock. The whole claim of
an analyzer is that its edges come from a real reading of real files, and a
mocked parser tests nothing.
