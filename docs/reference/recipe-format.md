# Recipe format

A recipe is one verification command plus the summariser that turns its output
into evidence. Recipes are Go values in `internal/recipe`; this page documents
the shape.

## Recipe

| Field | Type | Meaning |
|---|---|---|
| `Name` | `string` | Shown in results and gates. |
| `Kind` | `Kind` | `build`, `vet`, `test`, `race`, `lint`, `analyzer`, `format`, `custom`. Verification levels select on this. |
| `Argv` | `[]string` | The command. Never a shell string: a shell would make the sandbox's argument boundary meaningless. |
| `Dir` | `string` | Relative to the worktree root. |
| `Timeout` | `time.Duration` | A hung test must fail the step, not the system. |
| `Env` | `[]string` | Added to the sandboxed environment. |
| `Summarize` | `Summarizer` | Compresses output. Nil uses a generic summariser. |
| `AppliesTo` | `func(string) bool` | Whether the recipe is relevant to this worktree. Nil means always. |

## Verification levels

| Level | Kinds that must produce a passing result |
|---|---|
| `low` | build |
| `standard` | build, vet, test |
| `high` | build, vet, test, race, format |

`high` *runs* every kind, including `lint` and `analyzer`, but cannot *demand*
a result from those two: they are conditional on a configuration the repository
committed, so a repository with neither would fail `high` for checks that could
never have run. They are not thereby optional — a conditional check that runs
and fails blocks acceptance anyway. See the completion contract in
[the CLI reference](cli.md#the-completion-contract).

A level is what a task selects; the completion contract then requires a
**passing** result for every required kind against the **current** candidate.

Four of the kinds `high` selects run only where the repository asked for them:

| Kind | Asked for by |
|---|---|
| `lint` | a committed `.golangci.yml` |
| `analyzer` | rules under `semgrep/` |
| `generate` | a `generate:` entry in `.le/verify.yaml` |
| `integration` | an `integration:` entry in `.le/verify.yaml` |

Holding a change to rules a repository never adopted is a verdict its
maintainers did not agree to, so a repository that declared none of these gets
the same `high` as before. What is conditional is whether the check *exists* —
not whether its verdict counts: a conditional check that runs and fails blocks
acceptance like any other.

`generate` and `integration` are documented in
[declare runtime and generation checks](../how-to/declare-runtime-checks.md).
They are the two §10.1 rows that cannot be built-ins, because how to bring up a
database and which generator writes which files are facts about a repository
rather than about Go.

## Summarizer

```go
type Summarizer func(exitCode int, stdout, stderr string) (Status, Summary)
```

`Status` is `pass`, `fail`, `error` or `skipped`. The distinction between `fail`
and `error` is the one that matters: `fail` means the tool ran and found
something, `error` means the tool did not run. Reporting the second as the first
claims the code is fine when nothing checked it.

### Summary

| Field | Meaning |
|---|---|
| `Headline` | One line. It reaches the model and the gate. |
| `Findings` | Located problems: file, line, column, message, optional test name and rule id. |
| `Counts` | Tool-specific tallies. |
| `Truncated` | Set when findings were dropped, so a summary is never quietly partial. |

## Result

| Field | Meaning |
|---|---|
| `Recipe`, `Kind`, `Status`, `Summary` | as above |
| `ExitCode` | the raw exit status |
| `Candidate` | the worktree content hash this describes |
| `ArtifactHash` | the full output, content-addressed |

`Candidate` is what makes a result re-checkable. Evidence produced against an
older candidate is marked stale during recovery rather than being trusted: §7.2
forbids replaying a recipe whose outcome is uncertain without re-checking what
it examined.

## Shipped recipes

| Name | Kind | Applies when |
|---|---|---|
| `go build` | build | `go.mod` present |
| `go vet` | vet | `go.mod` present |
| `go test` | test | `go.mod` present |
| `gofmt` | format | `go.mod` present |
| `go test -race` | race | `go.mod` present |
| `semgrep` | analyzer | `semgrep/` holds rules **and** semgrep is installed |

See [how to write one](../how-to/write-a-recipe.md).
