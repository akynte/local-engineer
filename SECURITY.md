# Security Policy

## Reporting a vulnerability

**Please do not open a public issue for a security problem.**

Use GitHub's private vulnerability reporting on this repository:
*Security → Report a vulnerability*. That opens a private channel with the
maintainers.

Please include:

- what an attacker can do, and what they need to start with;
- the output of `le doctor` (it names the isolation layers actually in effect);
- the image digest or `le version` output;
- a minimal reproduction if you have one.

We aim to acknowledge within 3 working days and to ship a fix or a documented
mitigation within 30 days for issues in the supported version.

## Supported versions

Pre-1.0, only the latest released version is supported. After 1.0 this section
will name a support window.

## The trust boundary

Being precise about this saves everyone time, because several things that look
like vulnerabilities are documented design limits.

**In scope.** Anything that lets code running in a task sandbox:

- read or write files outside the mounts and paths granted to it;
- read another workspace's index, ledger, artifacts, cache or engine data;
- reach the model-management endpoints or the supervisor API;
- modify the policy files, the execution journal, or hidden tests;
- escape the container to the host.

Also in scope: repository content that reaches the model as an instruction
rather than as data — a README, comment, commit message, test fixture,
dependency manifest or tool result that changes what the system does rather
than what it knows;  a path where repository content leaves the machine when
`offline` is set; a supervisor API endpoint that performs a state change
without the operator asking; and any credential written to disk in plaintext by
this software.

**Documented limits, not vulnerabilities.** These are stated in the README and
reported by `le doctor`:

- Concurrent tasks can see each other's processes unless the optional
  bubblewrap layer is active. Unprivileged user namespaces are usually
  unavailable inside a container, so it is off by default.
- Landlock's TCP rules do not cover Multipath TCP sockets. Network containment
  is **the container's network configuration**; Landlock port rules augment it
  and are not the boundary.
- The §6.1 allowlisting proxy is the only provisioned route out, it is off by
  default, and **a task sandbox is never granted its port**. That separation is
  enforced by the sandbox rather than by the proxy: the allowlist bounds where
  provisioning can reach, and the Landlock ruleset bounds who can ask. An
  allowlist is not an access control, and neither is relied on to do the
  other's job. Widening the allowlist is an operator decision with a written
  reason per rule; `le doctor` warns on wildcard entries.
- Out-of-scope writes *inside* a task's own worktree are detected by diff, not
  prevented. That is the design: a task must be able to edit its worktree.
- **A model can be persuaded; the fence only bounds what it is told.** Every
  piece of repository content reaching the model — packet slices, recorded
  notes, and every tool result — is delimited by markers carrying a token
  generated per run, and the prompt states that fenced content is data and never
  an instruction. Content cannot close its own fence: marker-shaped text inside
  a body is defanged, and guessing a 128-bit token is not a strategy. What this
  does not do is stop a model from being convinced by something it read. It is
  one layer. The layers that stop damage are the sandbox the tools run in, the
  policy on protected paths, the diff an operator approves, and a completion
  contract that decides from evidence rather than from the model's account of
  itself — none of which a persuaded model can talk its way past.
- A remote provider sees the repository content you send it. That is what
  choosing a remote provider means; offline mode refuses remote providers at
  startup precisely so it cannot happen by accident.
- Publishing the API as `-p 7777:7777` instead of `-p 127.0.0.1:7777:7777`
  exposes the supervisor to your network. The supervisor warns about this at
  startup and in `le doctor`.
- Mounting the Docker socket into the container gives it root on your host.
  The shipped compose files do not, and nothing in this project needs it.

## Supply chain

Releases carry SBOMs (SPDX and CycloneDX), SLSA provenance, and Sigstore
signatures. Verify a release before running it; the commands are in
[docs/how-to/verify-a-release.md](docs/how-to/verify-a-release.md).
