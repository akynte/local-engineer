# DR-5: OpenCode as the execution engine

- Status: Superseded by [DR-7](0007-native-engine.md)
- Date: 2026-09-14

## 1. Problem

The supervisor needs an agent runtime that can hold a tool loop, edit files and
talk to an editor, without this project reimplementing all of it.

## 2. Alternatives

- Write a bespoke tool loop and editor integration.
- Adopt an existing open-source engine and supervise it.
- Use a vendor-hosted agent runtime.

## 3. Evidence

OpenCode is MIT licensed, so it is redistributable in the image alongside
Apache-2.0 code. It has a provider layer that already consumes the boundary
DR-4 chose, and it supports the Agent Client Protocol, which gives editor
integration without this project writing an editor plugin. A vendor-hosted
runtime contradicts the local-first premise and the offline mode.

## 4. Chosen

OpenCode, pinned by version, run per task inside the task sandbox with its XDG
directories pointed at the workspace's own `opencode/` directory so it can
never see another workspace's sessions.

## 5. Why

It removes the largest piece of undifferentiated work from this project's
scope, and the supervision model means the engine's behaviour is bounded by the
sandbox and recorded in the journal regardless of what it does internally.

## 6. Disadvantages

- **An upstream dependency on a fast-moving project.** A breaking change
  upstream is a breaking change here.
- **The engine's internal decisions are not journalled by this project**, only
  its observable effects. The journal records operations the supervisor drives;
  what happens inside one engine session is visible through its file writes and
  tool calls, not through its reasoning.
- Pinning a version means lagging upstream fixes until the pin is moved.
- A second runtime in the image (Node) that a pure-Go supervisor would not
  otherwise need.

## 7. Replacement path

The engine sits behind an adapter contract, and every guarantee that matters —
isolation, the journal, evidence, verification — is enforced by the supervisor
outside the engine. Replacing the engine means implementing the adapter
contract against a different runtime; the isolation and recovery guarantees do
not move.

**Status note (2026-09-15).** This record is superseded. The adapter contract
it argued for exists and is unchanged; the engine behind it is not OpenCode but
the native loop of [DR-7](0007-native-engine.md). The text above is left as
written, because a record edited to match what happened is not a record.
