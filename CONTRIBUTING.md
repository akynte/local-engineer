# Contributing

Thank you for considering a contribution.

## Before you start

- Read [docs/explanation/architecture.md](docs/explanation/architecture.md) and
  the decision records in [docs/adr/](docs/adr/). Most "why is it done this
  way" questions are answered there, and a change that contradicts a decision
  record needs a new record, not just a patch.
- For anything larger than a bug fix, open an issue first. A design discussion
  costs less than a rejected pull request.

## Development setup

The fastest path is the Dev Container (`.devcontainer/`), which pins the same
toolchain CI uses. Otherwise you need Go, Docker, and Node LTS for the
sidecars.

`go.mod` carries a `toolchain` directive naming the minimum patch release,
because earlier ones in the same series ship a standard library `govulncheck`
reports against this code. A default Go installation fetches that toolchain on
its own; if you have set `GOTOOLCHAIN=local`, install at least the version
`go.mod` names or the build will refuse to start.

The two linters are pinned, and the pins matter: `staticcheck` before v0.8 does
not build under Go 1.26, and `golangci-lint` v1 cannot read this repository's
v2 configuration.

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.13.2
go install honnef.co/go/tools/cmd/staticcheck@v0.8.1
```

```bash
git clone https://github.com/akynte/local-engineer
cd local-engineer
make check      # exactly what CI runs
```

`make check` must pass before you open a pull request. If it passes locally and
fails in CI, that is a bug in `make check` and we want to hear about it.

## The rules that are enforced by the build

Three invariants are not style preferences; the build fails on them.

1. **Workspace isolation.** `sql.Open` and file writes are confined to
   `internal/store` and `internal/artifacts`. The `storescope` analyzer
   enforces this. If you need to persist something, add an API to
   `internal/store` — do not add an exemption without a reason stated in the
   analyzer's `exemptions` map and agreed in review.
2. **Provenance on every retrieval result.** Every slice carries
   `workspace_id`, `repository_id`, `worktree_id`, path, symbol, content hash
   and index version. `retrieval.Guard` rejects anything missing one, and the
   isolation tests assert no packet ever carries a foreign slice.
3. **Intent before side effect.** Anything that changes the world journals its
   intent first and its outcome second. An operation with no outcome is
   *uncertain*, and recovery inspects rather than assumes. Do not add a code
   path that performs a side effect without a journal entry around it.

## Testing

- Unit tests run with `-race`. A test that needs a real kernel feature
  (Landlock, namespaces) must `t.Skip` with a reason when it is unavailable,
  never silently pass.
- Isolation tests live in `internal/store/isolation_test.go` and are the
  highest-value tests in the repository. If you touch storage, retrieval or
  the cache, check that they still fail when you deliberately break isolation
  — a test that cannot fail is not protecting anything.
- Integration tests that need Docker are tagged and run in CI.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org). The changelog is
generated from them, and a commit-lint job enforces the format.

```
feat(graph): add interface-to-implementation edges for Go
fix(store): refuse to open a database stamped with another workspace
docs(adr): record why the graph stays in SQLite
```

Breaking changes use `!` and a `BREAKING CHANGE:` footer.

## Sign-off

Commits must carry a Developer Certificate of Origin sign-off:

```bash
git commit -s -m "feat(graph): ..."
```

## Pull requests

- One logical change per pull request.
- Include tests. A bug fix without a regression test will be asked for one.
- Update the documentation in the same pull request. Docs live in `docs/` and
  the how-to pages are executed by CI, so a wrong command block fails the build.
- If you change behaviour described in a decision record, update the record or
  add a new one superseding it.

## What gets rejected

- Changes that widen the trust boundary without a decision record.
- New dependencies without a clear justification. This is a local-first tool;
  every dependency ends up in an SBOM and in a supply-chain review.
- Claims in documentation that are not backed by something in
  `docs/benchmarks/results/` or by a test.
- Prompt or model tweaks justified by a single example. Bring an evaluation.
