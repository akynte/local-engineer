# Write a recipe

A recipe is one verification command and the summariser that turns its output
into evidence.

Recipes are Go, in `internal/recipe`, not configuration. The reason is the
summariser: parsing `go test` output into per-test findings, or a race report
into its two conflicting stacks, is code. Expressing it in YAML would mean
inventing a pattern language, and a pattern language is a worse Go.

## Adding one

```go
{
    Name: "golangci-lint", Kind: KindLint, AppliesTo: isGo,
    Argv:    []string{"golangci-lint", "run", "./..."},
    Timeout: 5 * time.Minute,
    Summarize: GolangciLint,
},
```

Four things to get right, each because getting it wrong is quiet:

**`Kind`** decides which verification levels select it. `low` is build only;
`standard` adds vet and test; `high` adds everything. A recipe with the wrong
kind either never runs or runs when nobody wanted it.

**`AppliesTo`** skips repositories the recipe cannot check. A Go linter pointed
at a TypeScript repository does not fail usefully — it fails confusingly, and a
confusing failure in verification is worse than a missing check because someone
has to work out which it was.

It carries a second job for any recipe wrapping a tool the image may not have:
check `exec.LookPath` too, as `HasGolangciConfig` and `HasSemgrepRules` do. A
machine without the tool should *skip* the recipe, not fail verification — a
missing toolchain says nothing about the code. And note that the engine has no
shell tool, so anything a recipe invokes has to be in the image; see
[the image manifest](../reference/image-manifest.md).

**`Timeout`** bounds it. A hung test must fail the step, not the system.

**`Summarize`** is the part that matters.

## The summariser rule

A tool that *failed to run* is not a tool that found nothing.

```go
func GolangciLint(exitCode int, stdout, stderr string) (Status, Summary) {
    findings := parse(stdout)
    if exitCode == 0 {
        return Pass, Summary{Headline: "no lint findings"}
    }
    if len(findings) == 0 {
        // Non-zero exit and nothing parseable: say so rather than claiming
        // the code is fine. "We could not check" and "there is nothing wrong"
        // are different sentences and only one of them is true here.
        return Generic(exitCode, stdout, stderr)
    }
    return Fail, Summary{
        Headline: fmt.Sprintf("%d lint finding(s)", len(findings)),
        Findings: findings,
    }
}
```

Every shipped summariser follows this, and the semgrep one goes further: a
semgrep *error* — an unparseable rule, a file it could not read — is reported as
an error rather than as a verdict on the code.

## Why the order is fixed

`GoRecipes` returns build first, so a compile failure short-circuits the rest
rather than producing a page of test noise about code that never built. That is
a property of the set, not of any entry, which is another reason the set is code.

## Testing it

Summarisers are pure functions of `(exitCode, stdout, stderr)`, so test them
against real captured output rather than against a mock:

```go
func TestSummariserReportsAFailedRunAsAnError(t *testing.T) {
    status, _ := GolangciLint(127, "", "golangci-lint: command not found")
    if status == Pass {
        t.Fatal("a tool that did not run was reported as a pass")
    }
}
```
