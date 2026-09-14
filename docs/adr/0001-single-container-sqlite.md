# DR-1: Single-container distribution with SQLite storage

- Status: Accepted
- Date: 2026-09-14

## 1. Problem

Users must run the system with one command and keep data across restarts.

## 2. Alternatives

- Multi-container compose: application, database, inference.
- Single container with an embedded PostgreSQL managed by s6-overlay.
- Single container with SQLite.

## 3. Evidence

SQLite is embedded, transactional and has an online backup path. The workloads
are single-user and small: structural queries, lexical search, optional vector
search over 10⁴–10⁶ chunks, a transactional task ledger, telemetry, and
content-addressed artifacts. PostgreSQL inside a container needs a second init
system, its own upgrade handling and its own backup story, for a workload that
never has a second concurrent user. The split compose file preserves the
conventional option for anyone who wants it.

## 4. Chosen

A single container, SQLite files per workspace, and `le` as the process
supervisor.

## 5. Why

It is the simplest reproducible install: one volume to back up, no daemon
lifecycle inside the container, and no ordering problem between an application
and a database that must both be healthy before work can start.

## 6. Disadvantages

- **Several processes under one PID 1 reduce orchestrator visibility.** Compose
  and Kubernetes see one container where they would normally see three, so
  their per-process restart and health signals do not apply.
- **SQLite needs a real filesystem with working `fsync`.** A container overlay
  layer or a network share breaks its durability guarantees.
- **No server mode.** There is no path here to multiple users against one
  instance.

The first is compensated: the supervisor is itself a process manager with
health checks and a restart budget, `/readyz` reports per-child state, and the
dashboard shows it. The second is checked: `le doctor` fails when `/data` is on
an overlay or a network filesystem, and the entrypoint warns before the
supervisor even starts.

## 7. Replacement path

Storage sits behind the `internal/store` interfaces. The split compose variant
(`deploy/docker-compose.split.yml`) is maintained and validated in CI, so the
conventional layout stays a supported configuration rather than an aspiration.
A PostgreSQL implementation can be added behind the same interfaces if a server
mode is ever wanted.
