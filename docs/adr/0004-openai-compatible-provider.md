# DR-4: OpenAI-compatible HTTP as the provider boundary

- Status: Accepted
- Date: 2026-09-14

## 1. Problem

Model and backend independence: the system must not be tied to one inference
engine or one vendor.

## 2. Alternatives

- A native SDK per vendor.
- A custom RPC protocol.
- OpenAI-compatible HTTP plus a small capability declaration.

## 3. Evidence

llama.cpp, vLLM, SGLang, Ollama, LM Studio, TGI and the cloud gateways all
expose the OpenAI-compatible surface, and the execution engine's provider layer
already consumes it. A native SDK per vendor would mean a dependency tree per
vendor in every build and every SBOM, for a local-first tool where the remote
lanes are opt-in and disabled in offline mode.

## 4. Chosen

OpenAI-compatible HTTP plus an explicit `Capabilities()` declaration.
Grammar-constrained and schema-constrained calls are made only against
providers that advertise them.

## 5. Why

The broadest reach for the least code. Adding a provider is one Go file
implementing `Provider` plus a capability declaration.

## 6. Disadvantages

- **Feature gaps between providers are hidden behind one API.** Thinking
  control, infill and grammars are not universal, and a single interface makes
  them look like they are.
- A vendor whose wire format differs (Anthropic's Messages API, for instance)
  needs a native implementation anyway, so "one boundary" is not literally one
  implementation.
- Capability declarations are assertions. A misdeclared provider fails at call
  time rather than at configuration time.

The first is handled structurally rather than by documentation:
`ChatStructured` returns a typed `UnsupportedError` naming the missing
capability when a provider does not declare structured output. It never falls
back to prompt-only instructions, because a silent degradation is exactly the
failure this disadvantage predicts.

## 7. Replacement path

`llm.Provider` is an interface. A native provider implements it directly —
`internal/llm/anthropic.go` already does, for a vendor whose wire format is not
OpenAI-compatible — and role routing in `providers.yaml` points at it with no
change to any caller.
