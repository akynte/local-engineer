# Known limitations

What this does not do, stated in one place so nobody has to infer it.

`le doctor` reports the runtime ones at startup.

## The evaluation settles nothing yet

A real run exists — 3 tasks × 4 arms × 5 passes, 60 runs against a local model
on disclosed hardware — and its own conclusion is that the task set cannot
answer the questions the arms were built to ask:

- every arm's confidence interval overlaps every other's;
- 4 of 12 task/arm cells changed verdict between passes, so a cell disagrees
  with itself;
- two of three tasks were solved by every arm on every pass, including the bare
  baseline, so they carry no information.

A larger fixture and five harder tasks now exist, and a probe over them shows
separation without statistical significance. **The graph's contribution remains
measurable rather than measured.** See
[the results](../benchmarks/results/README.md).

## Isolation

- **Process-level isolation between concurrent tasks needs bubblewrap**, which
  is usually unavailable inside a container. Without it, concurrent tasks share
  a PID view.
- **Landlock's network rules do not cover Multipath TCP**, and Go's `net.Listen`
  uses MPTCP by default. Port rules are augmented by the container's network
  configuration, not relied on alone.
- **A task never reaches the network, and that is not negotiable per task.**
  Verification runs with `GOPROXY=off`. A change needing a new dependency is
  `le deps sync` through the §6.1 proxy, run by an operator between tasks, with
  the `go.mod` diff visible before any task verifies against it. There is no
  flag that gives a task egress, because a flag is a thing that gets set: the
  only function that builds a sandbox spec containing the proxy port takes a
  `proxy.Lane`, which a task runner cannot construct.
- **The egress proxy is off by default and its allowlist is small.** Four hosts
  ship. Adding one is an operator decision that requires a written reason, and
  `le doctor` warns about wildcard rules because those are the entries most
  likely to be broader than intended.
- **The proxy sees plaintext for `http://` and bytes for `https://`.** A CONNECT
  tunnel is deliberately opaque: the proxy decides on the host and then carries
  bytes it does not inspect. It is an allowlist, not an inspecting gateway, and
  it does not terminate TLS.
- **Out-of-scope writes inside a worktree are caught by diff and policy, not by
  the sandbox.** The task legitimately has write access to what it is editing.

## Storage

- **SQLite needs a real filesystem.** Not an overlay layer, not a network share.
  `le doctor` fails if you put the data directory on one.
- **Downgrades across schema versions are not supported.** Migrations are
  forward-only; `le backup` before every upgrade.

## Language coverage

- **TypeScript is type-checked only with the sidecar.** The `cpu` and `cuda`
  images carry it; the `-slim` image and host installs without Node do not.
  Without it the analyzer reads lexically and emits **no call graph at all**,
  because without a checker an identifier in call position may be a local or a
  shadowed binding. Every edge says which reading produced it, and `le index`
  says out loud when it fell back.
- **Dynamic SQL is not resolved.** A query assembled at run time is recognised
  as a database call, but naming a table would be guessing.
- **Computed routes and URLs produce no edge.** A literal is required, because a
  guessed endpoint is worse than a missing one.
- **Helm templates are recorded, not parsed.** Rendering a chart needs values
  the analyzer does not have, and a half-rendered template read as YAML
  produces confident nonsense.

## Models

- **Reasoning models can spend an entire output budget thinking.** At 34 tok/s a
  single call can take four minutes. Truncation is reported as its own outcome
  rather than as the model deciding to stop, but the budget is yours to set.
- **A provider that misdeclares its capabilities breaks things at run time.**
  `le models conformance` checks; nothing forces you to run it.
- **The shipped hardware profiles are starting points, not measurements.** They
  were written for named hardware, not yours. `le models bench --write` measures
  your machine and writes a profile from it; `le doctor` warns until you do.

## Runtime and generation checks

- **Nothing runs a browser out of the box.** `.le/verify.yaml` can declare an
  integration step that drives one, but no image ships browsers, because no
  image ships a UI for them to drive.
- **A generate check runs the generator on every `high` verification.** For a
  slow generator that is real time on every attempt; `timeout_minutes` bounds
  it, and the 60-minute cap bounds that.
- **`Recipe.AppliesTo` sees a worktree, not a diff.** A check that wants to
  compare against a base branch — `buf breaking`, `oasdiff` — has to fetch that
  base itself inside its own command, and a task sandbox has no network. In
  practice that means comparing against something already in the repository,
  such as a committed baseline schema.

## Deferred to a document that is not here

v3 defers three things to v2.0, which is not in this repository:

- the **1.0 acceptance matrix** (§1.2 refers to "the v2.0 Section 18 matrix");
- **Stage A, C and E** of the evaluation, named in §14 and §15 and defined
  nowhere in v3;
- the contents of **`workflows/`**, listed in the layout and described nowhere.

These are recorded rather than invented. `le models conformance` is built from
how v3 *uses* Stage A and is named for what it does.
