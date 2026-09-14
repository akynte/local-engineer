# Governance

## Current model: benevolent dictator, stated plainly

This project is young. Decisions are currently made by the maintainers listed
in [MAINTAINERS.md](MAINTAINERS.md), in practice by the project lead. Saying so
is more useful than describing a committee that does not exist.

This document will be revised when the contributor base makes a broader model
worth having, and the revision will itself be a decision record.

## How decisions are made

**Ordinary changes** — bug fixes, documentation, tests, dependency updates —
need one maintainer approval.

**Architectural changes** need a decision record in
[docs/adr/](docs/adr/) using the seven-point format:

1. Problem
2. Alternatives
3. Evidence
4. Chosen
5. Why
6. Disadvantages
7. Replacement path

Point 6 is not optional. A record that lists no disadvantages has not been
thought through, and a record with no replacement path is a trap for whoever
maintains this later.

**Changes that contradict an existing record** need a new record that
supersedes it, explaining what changed in the evidence. Records are never
edited to make a past decision look better; they are superseded.

## What needs evidence, not opinion

Several categories of change are decided by measurement:

- Retrieval and prompt changes: by an evaluation, not by an example.
- Storage or graph backend changes: by the benchmarks in `evals/storage/` and
  `evals/graph/`.
- Model routing beyond one model for all roles: by measured benefit per role.
- Performance claims in the README: by numbers in
  `docs/benchmarks/results/` with the hardware disclosed.

"It felt better" is not evidence, and neither is a single successful task.

## Releases

Semantic versioning. `0.y.z` until the acceptance matrix passes end to end,
then `1.0.0`. Any maintainer may cut a release; the release workflow signs
artifacts, attaches SBOMs and provenance, and generates the changelog from
Conventional Commits.

## Becoming a maintainer

Sustained, high-quality contribution over time, plus demonstrated care for the
invariants in [CONTRIBUTING.md](CONTRIBUTING.md). Existing maintainers propose
and decide. There is no fixed contribution count.

## Code of conduct

[CODE_OF_CONDUCT.md](CODE_OF_CONDUCT.md) applies to everyone, maintainers
included.
