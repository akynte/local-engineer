# DR-3: Container boundary plus Landlock, bubblewrap optional

- Status: Accepted
- Date: 2026-09-14

## 1. Problem

Isolate task execution inside a container, without privileges.

## 2. Alternatives

- Host bubblewrap with user namespaces (the previous design's assumption).
- gVisor or Kata Containers.
- Landlock.
- Docker-in-Docker with sibling containers.
- No per-task isolation.

## 3. Evidence

Landlock is unprivileged, has been in mainline since 5.13, gained TCP
restrictions in 6.7, and has a maintained Go library. Unprivileged user
namespaces are frequently unavailable inside a container: measured on an
Ubuntu host, `kernel.apparmor_restrict_unprivileged_userns` is 1 by default,
and inside Docker's default configuration the `bwrap` probe fails outright.
Mounting the Docker socket would give the container root on the host and is
therefore not an option at all. gVisor and Kata are strong but change the
deployment shape and are not available on every host.

## 4. Chosen

Three layers, with `le doctor` reporting which are actually active:

1. the container boundary (always),
2. Landlock per task (default inside the container),
3. bubblewrap per task (optional, when user namespaces are available).

## 5. Why

It works in the default `docker run` with no special flags, it degrades
gracefully instead of failing, and the strongest mode remains available to
anyone who can enable it.

## 6. Disadvantages

- **Without namespaces, concurrent tasks share a PID view.** One task can see
  another's processes. This is the one row of the guarantee table that only the
  optional layer satisfies, and the README says so in those words.
- **Landlock's network coverage is incomplete.** Its TCP rules do not cover
  Multipath TCP sockets, and Go's `net.Listen` has defaulted to MPTCP since Go
  1.24 — so a sandboxed Go program can still listen on an unlisted port. Port
  rules are augmentation, not the boundary.
- **The seccomp profile must permit the three Landlock syscalls.** Docker's
  default profile does; another runtime's might not, and then layer 2 silently
  becomes unavailable.
- The re-exec helper adds a process to every sandboxed command.

Each of these is reported at runtime rather than left in a document:
`le doctor` prints the Landlock ABI, why bubblewrap is unavailable, and the
Multipath TCP caveat every single time.

## 7. Replacement path

`sandbox.Runner` is an interface, and `sandbox.Select` picks the strongest
available implementation. A gVisor or microVM runner for untrusted
repositories is a new type implementing the same three methods; no caller
changes.
