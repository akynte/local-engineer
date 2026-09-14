# Configure models

The system is model-agnostic by construction: two boundaries, both
configuration. You can change the model, the engine behind it, or the vendor,
without touching code.

## The two boundaries

1. **Provider** — an OpenAI-compatible HTTP endpoint, plus an explicit
   declaration of what that provider can actually do (tool calling, structured
   output, grammars, infill, embeddings, thinking control).
2. **Role routing** — roles (planning, coding, review, classification, …) map
   to a provider and model. The shipped state maps *every* role to one model.

Multi-model routing is off by default. Turn it on per role only when you have
measured that it helps.

## Embedded inference

Put a GGUF under `/data/models`, then edit `/data/config/le.yaml`:

```yaml
inference:
  mode: embedded
  binary: llama-server
  port: 8080
  start_timeout_seconds: 600
  args:
    - --model
    - /data/models/your-model.gguf
    - --host
    - 127.0.0.1
    - --port
    - "8080"
    - --ctx-size
    - "32768"
    - --n-gpu-layers
    - "999"
    - --flash-attn
    - "on"
```

`le api` supervises `llama-server` as a child: it waits for `/health` before
reporting ready, restarts it with backoff if it dies, and stops it in order on
shutdown. `start_timeout_seconds` matters — a cold GGUF can take minutes to
load, and a short timeout looks like a crash loop.

## External inference

Point at a server you run — vLLM, SGLang, Ollama, LM Studio, TGI, or a
llama-server elsewhere:

```yaml
inference:
  mode: external
  base_url: http://127.0.0.1:8080
```

and in `/data/config/providers.yaml`:

```yaml
default: local
providers:
  - name: local
    kind: llamacpp
    base_url: http://127.0.0.1:8080
    model: your-model
```

For an engine that is not llama.cpp, declare what it supports. This is not
paperwork: the supervisor refuses a schema-constrained call against a provider
that does not declare structured output, rather than silently falling back to
asking nicely in the prompt.

```yaml
providers:
  - name: vllm
    kind: openai_compatible
    base_url: http://127.0.0.1:8000
    model: Qwen3-Coder-30B
    capabilities:
      tool_calling: true
      structured_output: true
      embeddings: false
      max_context: 32768
      local: true
```

## A remote provider

Off by default, and refused outright in offline mode.

```yaml
default: local
providers:
  - name: local
    kind: llamacpp
    base_url: http://127.0.0.1:8080
    model: your-model
  - name: cloud
    kind: anthropic
    model: claude-opus-5
    api_key_env: ANTHROPIC_API_KEY
roles:
  # One role on a remote model, everything else local.
  verification: cloud
```

**Never put a secret in `providers.yaml`.** `api_key_env` names an environment
variable, so the file stays safe to commit and to attach to a bug report. A
provider whose named variable is unset fails at startup, not at first call.

Be clear about what this means: repository content in a packet sent to a remote
provider leaves your machine. The isolation guarantees cover your host and your
other workspaces, not a third party.

## Offline mode

```yaml
offline: true
```

With this set, constructing the router refuses any provider that is not local —
at startup, so a misconfiguration cannot cause a silent egress later. See
[run fully offline](run-offline.md).

## Check it

```console
$ le models health
local                ok
```

```console
$ le config show
```

This prints the effective configuration, the declared providers and the
resolved role routing, so you can see what will actually be used rather than
what you meant to configure.

## Next

Measure the model on your machine and generate a profile from what you see:
[choose a hardware profile](choose-a-profile.md).
