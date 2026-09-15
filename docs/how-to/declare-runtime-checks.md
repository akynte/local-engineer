# Declare runtime and generation checks

Design v3 §10.1 adopts two checks that no built-in recipe can express, because
neither is a fact about Go:

- **Runtime feedback** — "browser, disposable DB, integration", the only way to
  catch *compiles but wrong*.
- **Deterministic generation for boilerplate** — sqlc, buf, OpenAPI clients —
  with "contract checks still apply".

Your repository declares both in `.le/verify.yaml`. Like `.golangci.yml` and
`semgrep/`, it is opt-in: a repository that declares nothing is verified
exactly as before.

Both run at `--verify high` only.

## The file

```yaml
version: 1

# Generators whose committed output must already be current.
generate:
  - name: sqlc
    argv: ["sqlc", "generate"]
    outputs: ["internal/store/gen"]
  - name: openapi
    argv: ["oapi-codegen", "-config", "api/cfg.yaml", "api/openapi.yaml"]
    outputs: ["internal/api/gen.go"]

# Checks that exercise the change against something running.
integration:
  - name: postgres
    up:    ["./scripts/testdb.sh", "up"]
    test:  ["go", "test", "-tags=integration", "./..."]
    down:  ["./scripts/testdb.sh", "down"]
    ports: [5432]
    timeout_minutes: 15
```

## Project analyzers

`check:` runs a repository's own analyzers. It is `KindAnalyzer`, like the
built-in semgrep recipe, so a finding blocks acceptance and a missing tool
skips rather than fails.

```yaml
check:
  - name: gosec
    argv: ["gosec", "-quiet", "./..."]
  - name: migrations
    argv: ["squawk", "migrations/"]
    requires: squawk
  - name: proto-breaking
    argv: ["buf", "breaking", "--against", ".git#branch=main"]
```

`requires` names the binary the check needs; it defaults to `argv[0]`. When it
is not installed the check **skips** — a missing tool says nothing about the
code, which is the rule every recipe follows.

The image ships `gosec`, `gitleaks`, `osv-scanner`, `squawk`, `buf` and
`oasdiff` for exactly this. None has a built-in recipe, because which tables a
migration may lock and which proto changes are breaking are facts about a
repository.

## Generation is a check, never a step

The generator runs, its declared outputs are compared with what was committed,
and then **the worktree is restored exactly as it was found**.

That restore is what makes this safe to run inside verification. A generator
that left its output behind would do two damaging things: every result recorded
before it would become evidence about a candidate that no longer exists (§7.2),
and the generator's output would appear in the model's diff at the human gate.

So what the check reports is a property, not a mutation: *does the committed
generated code match what the generator produces now?* That catches a model
hand-editing generated output, and a schema change nobody regenerated.

`outputs` is required and cannot be inferred. The check restores those paths,
and restoring a path the generator never wrote would be a way to lose work. A
path that escapes the worktree is refused.

Restoring works in both directions: files the generator changed are rewritten
with their original content **and mode**, and files it *created* are removed.
The second half is the one that matters most — a schema gaining a table is
exactly when a generator adds a file, and leaving it behind is the mutation the
whole mechanism exists to avoid.

If the restore itself fails, the step reports **fail**, not error. An error
does not block acceptance, and an unverifiable worktree must.

Generation runs **before** build. A repository whose generated code is stale
fails the build with a message about the generated file rather than about the
schema that moved, and the useful message is this one.

## Integration is up, test, down — with down guaranteed

`down` runs even when `test` failed, and even when `up` failed partway. It gets
its own timeout, because the moment you most need teardown is when the step
timed out with something still running. Leaving a database or a container
behind turns one failure into every later run failing for a different reason.

`ports` are granted by the sandbox for both bind and connect, and **only those**.
They are declared rather than discovered because a rule for a port nobody named
would either be missing when needed or wider than intended. A step that binds an
undeclared port gets `bind: permission denied` — that is the sandbox working.

## What counts as a failure

The distinction the whole package rests on applies here too:

| Situation | Status | Why |
|---|---|---|
| `test` exits non-zero | **fail** | a verdict on the code |
| `up` fails | **error** | a database that would not start says nothing about the change |
| the generator is not installed | **error** | says nothing about whether the output is current |
| generated output differs | **fail** | the committed code is not what the generator produces |
| the worktree could not be restored | **fail** | the worktree is no longer what any other result measured, so it must block |

An `error` does not block acceptance; a `fail` does — for these kinds as much
as for `go test`, even though neither is on the level's *required* list. What is
conditional is whether the check exists, not whether its verdict counts. See
[the completion contract](../reference/cli.md#the-completion-contract).

## About browsers

§10.1 names a browser among the runtime checks. The mechanism here runs one:
declare an integration step whose `test` drives Playwright or similar, with the
ports it needs.

No image ships a browser, because no image ships a UI for one to drive — see
[verify item 5](../reference/verify-list.md). If you install one on the host or
in a derived image, an integration step is how you use it.

## Limits

- `version` must be `1`. A future shape is refused clearly rather than parsed
  into something half-right.
- A step may not ask for more than 60 minutes. A repository cannot ask
  verification to hang: the point of a bounded check is that it is bounded.
- A `check:` step has no `outputs` and does not restore anything. If your
  analyzer rewrites files, it is a generator — declare it under `generate:`.
- Step names must be unique within their kind.
- An integration step with no `test` is refused — a step that brings something
  up and checks nothing is a slow no-op.

## See also

- [Write a recipe](write-a-recipe.md) — for checks that belong in the tool
  rather than in one repository.
- [Recipe format](../reference/recipe-format.md) — kinds and levels.
