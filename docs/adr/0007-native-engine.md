# DR-7: A native engine on the provider boundary, superseding DR-5

- Status: Accepted
- Date: 2026-09-15
- Supersedes: [DR-5](0005-opencode-engine.md)

## 1. Problem

DR-5 chose OpenCode as the execution engine and was written before any engine
existed. An engine has since been built, and it is not OpenCode. A decision
record that still names OpenCode, and a status note still saying the engine
"is the next phase", describe a repository that no longer exists.

## 2. Alternatives

- **Implement the OpenCode adapter as DR-5 specified**, and keep the native
  loop out of the repository.
- **Ship the native engine and leave DR-5 in place**, treating the difference
  as an implementation detail.
- **Supersede DR-5**, keeping the adapter contract it argued for and recording
  that the first implementation behind that contract is a native one.

## 3. Evidence

What DR-5 was actually about survived: the adapter contract (`internal/engine`)
exists, and every guarantee that matters — isolation, the journal, evidence,
the completion contract — is enforced by the supervisor outside the engine, as
DR-5's replacement path promised.

What did not survive was the reasoning for OpenCode specifically. DR-5's case
rested on removing undifferentiated work and on ACP giving editor integration
for free. Against a built system:

- The tool loop is not undifferentiated here. §8.1 requires the supervisor to
  decide what the model sees, and §10.1 rejects reflection without new
  evidence; both are properties of the loop, not of the harness around it. An
  engine that owns its own loop has to be constrained from outside rather than
  built correctly from the start.
- The provider abstraction (DR-4) already existed, with capability declarations
  and both tool-calling wire formats. A native engine consumes it directly and
  works with any OpenAI-compatible backend.
- A native engine is testable end to end without an external process, which is
  what let the Stage D interruption suite kill a real child and assert on the
  worktree rather than on a mock.
- Editor integration did not need the engine. §4.3's ACP-over-TCP bridge
  carries bytes to any configured agent's stdio without parsing the protocol.

The cost is equally real: this project now maintains a tool loop, and
`§16 verify item 3` (ACP transport for `opencode acp`) can never be resolved as
written.

## 4. Chosen

`internal/engine/native`: a bounded tool loop over the DR-4 provider boundary,
with a capped tool surface, worktree confinement, and verification wired in as
the correction signal. DR-5's adapter contract is kept exactly as it was.

## 5. Why

It keeps the part of DR-5 that was load-bearing — an engine behind a contract,
judged by evidence — while removing a Node runtime, an external process and an
upstream pin from the critical path of every task.

## 6. Disadvantages

- **This project now owns a tool loop**, which DR-5 explicitly wanted to avoid.
  Every capability an engine like OpenCode would have brought is now work here.
- **The tool surface is small** (nine tools) and deliberately so for small
  models; a user wanting a richer agent has nothing to switch to yet.
- **No ACP-speaking engine ships**, so the bridge of §4.3 is off by default and
  carries nothing until an operator configures an agent.
- **The comparison with OpenCode was never measured.** The reasoning above is
  an argument, not an evaluation, and §16 item 3 records that it cannot be
  settled as the design framed it.

## 7. Replacement path

Unchanged from DR-5, and now demonstrated rather than asserted: the adapter
contract is `engine.Engine`, and an OpenCode adapter remains a legitimate
second implementation of it. The isolation, journal and verification guarantees
sit outside the engine, so swapping one moves no guarantee.
