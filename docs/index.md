# local-engineer documentation

This documentation follows the [Diátaxis](https://diataxis.fr) split. Each
section answers a different kind of question, so start with the one that
matches what you are doing.

## Tutorials — learning by doing

| Page | What you end up with |
|---|---|
| [First task in 15 minutes](tutorials/first-task.md) | A running container, an indexed repository, and a graph you can query |
| [Index a Go microservice](tutorials/index-a-go-service.md) | A worked example on a real service layout |

## How-to guides — solving a specific problem

| Page | |
|---|---|
| [Install](how-to/install.md) | Prerequisites and the three install shapes |
| [Configure models](how-to/configure-models.md) | Pointing at a model, embedded or external |
| [Choose a hardware profile](how-to/choose-a-profile.md) | Measuring your machine instead of guessing |
| [Persistent storage](how-to/persistent-storage.md) | Where data lives and what must not host it |
| [Start, stop and update](how-to/start-stop-update.md) | Lifecycle, including the shutdown contract |
| [Back up and restore](how-to/backup-and-restore.md) | Before every upgrade |
| [Run fully offline](how-to/run-offline.md) | No egress at all |
| [Add a provider](how-to/add-a-provider.md) | Extending the provider boundary |
| [Verify a release](how-to/verify-a-release.md) | Signatures, SBOM, provenance |
| [Troubleshooting](how-to/troubleshooting.md) | When `le doctor` reports a problem |

## Reference — the details

| Page | |
|---|---|
| [CLI](reference/cli.md) | Every command and flag |
| [Configuration](reference/configuration.md) | `le.yaml`, `providers.yaml`, profiles |
| [Graph schema](reference/graph-schema.md) | Node kinds, edge kinds, evidence categories |
| [HTTP API](reference/http-api.md) | The supervisor's endpoints |
| [Storage layout](reference/storage-layout.md) | What is on disk and why |

## Explanation — why it is built this way

| Page | |
|---|---|
| [Architecture](explanation/architecture.md) | The C4 view and the process model |
| [Isolation model](explanation/isolation-model.md) | What each layer guarantees, and what it does not |
| [Why small models can work here](explanation/why-small-models.md) | The argument, and how it will be tested |
| [Crash recovery](explanation/crash-recovery.md) | Intent-first journalling |
| [Decision records](adr/) | Every architectural decision, in a fixed format |

## A note on the command blocks

Command blocks marked with `<!-- test:run -->` in the source are executed by CI
against the CPU image. If a command in this documentation is wrong, the build
fails. Blocks that CI cannot run (anything needing a GPU, a model download, or
a published image) are marked `<!-- test:skip … -->` with the reason, so an
unrunnable example is visible rather than quietly rotting.
