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

| Level | Kinds required |
|---|---|
| `low` | build |
| `standard` | build, vet, test |
| `high` | every kind |

A level is what a task selects; the completion contract then requires a
**passing** result for every required kind against the **current** candidate.

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
