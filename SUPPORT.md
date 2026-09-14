# Support

## Before asking

Run `le doctor` and read its output. Most problems — an unwritable volume, a
data directory on an overlay layer, a missing isolation layer, a stale index,
a profile that does not fit the machine — are named directly, with the fix.

Then check [docs/how-to/troubleshooting.md](docs/how-to/troubleshooting.md).

## Where to ask

| What | Where |
|---|---|
| A bug | [Open a bug report](../../issues/new?template=bug.yml) |
| A feature idea | [Open a feature request](../../issues/new?template=feature.yml) |
| "Does model X work?" | [Open a model report](../../issues/new?template=model-report.yml) — these build the compatibility table |
| A question | [Discussions](../../discussions) |
| A security problem | **Not an issue.** See [SECURITY.md](SECURITY.md) |

## What to include in a bug report

The issue form asks for these, and a report without them usually cannot be
acted on:

- `le doctor --json` output (it contains no repository content)
- `le version`
- the image digest, or the commit if you built it yourself
- what you expected, and what happened instead

## What this project does not support

- Running `/data` on a network share or a container overlay layer. SQLite's
  durability guarantees do not hold there, and `le doctor` fails the check.
- Downgrading across a schema version. Migrations are forward-only; restore a
  backup instead.
- Mounting the Docker socket into the container.

## Response expectations

This is a young project with a small maintainer set. Issues are read, but a
response may take a week. Pull requests with tests are the fastest route to a
fix.
