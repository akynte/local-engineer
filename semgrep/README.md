# Project-invariant rules

§10.1 adopts "static-analysis and project-invariant analyzers (`go/analysis`,
semgrep)" for the problem it names: **domain rules the model does not know**.

The two halves are different tools for different jobs.

`go/analysis` is for invariants that need types. `storescope` is the one that
matters most here — it enforces §2.3's rule that persistence stays inside
`internal/store`, and it needs to know what `sql.Open` resolves to, not just
that the characters appear.

semgrep is for invariants that are about *shape*, where a type checker has
nothing to say and a human reviewer is the only other option. The rules in this
directory are those: patterns that are legal Go, compile cleanly, pass vet, and
are still wrong for this codebase.

## Why these rules and not more

Every rule here costs something. A rule that fires on correct code teaches the
model — and the human — to ignore the tool, which is worse than not having it.
So each rule below states the mistake it catches and why a compiler cannot.

Rules are run by the `semgrep` recipe at the `analyzer` kind. The recipe skips
itself when semgrep is not installed rather than failing: semgrep is a useful
addition, not a dependency of the build.

## Writing one

```yaml
rules:
  - id: a-short-kebab-case-name
    languages: [go]
    severity: WARNING          # ERROR fails the recipe; WARNING reports
    message: >-
      What is wrong, and what to do instead. This text reaches the model as a
      finding, so it should read as an instruction rather than a complaint.
    patterns:
      - pattern: ...
```

Test a rule against the codebase before committing it:

```console
$ semgrep --config semgrep/ --error .
```
