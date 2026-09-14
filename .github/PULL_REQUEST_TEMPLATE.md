## What this changes

<!-- One paragraph. What is different after this PR that was not before? -->

## Why

<!-- Link the issue, or state the problem. If this contradicts a decision
     record in docs/adr/, say which one and link the superseding record. -->

## How it was verified

<!-- Not "it works". What did you run, and what did it show? -->

- [ ] `make check` passes locally
- [ ] New or changed behaviour has a test
- [ ] If this touches storage, retrieval or the cache: the isolation tests in
      `internal/store/isolation_test.go` still fail when isolation is
      deliberately broken

## Invariants

Tick the ones that apply, or explain why they do not:

- [ ] No new `sql.Open` or file write outside `internal/store` /
      `internal/artifacts` (the `storescope` analyzer enforces this)
- [ ] Every retrieval result still carries full provenance
- [ ] Every side effect still journals intent before and outcome after
- [ ] No new dependency, or the dependency is justified below
- [ ] Documentation updated in this PR (how-to command blocks are executed by CI)

## Anything reviewers should look at closely

<!-- Be specific. "The backoff logic in supervise()" beats "everything". -->
