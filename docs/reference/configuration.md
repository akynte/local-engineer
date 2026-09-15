# Configuration reference

Three files under `/data/config/`:

- `le.yaml` — how the supervisor runs
- `providers.yaml` — model providers and role routing
- `profiles/*.yaml` — everything that depends on your hardware

```console
$ le config init      # write the defaults
$ le config show      # the effective configuration, as JSON
```

## `le.yaml`

```yaml
profile: reference-8gb-cuda-64gb-ram
offline: false

api:
  addr: 127.0.0.1:7777
  shutdown_grace_seconds: 30

inference:
  mode: embedded          # embedded | external | none
  binary: llama-server
  port: 8080
  start_timeout_seconds: 300
  args: []
  # base_url: http://127.0.0.1:8080   # when mode is external

sandbox:
  mode: auto              # auto | landlock | bwrap | none
  read_only_paths:
    - /usr
    - /bin
    - /lib
    - /usr/local/go
  allowed_tcp_connect: []

index:
  max_file_bytes: 1048576
  chunk_lines: 60
  watch_enabled: true
  excludes: [".git", "node_modules", "vendor", "dist", "build"]
egress:
  enabled: false
  deps_port: 7780
  docs_port: 7781
  allowlist:
    rules:
      - host: proxy.golang.org
        lanes: [deps]
        why: the Go module proxy
      - host: pkg.go.dev
        lanes: [docs]
        why: Go package documentation
```

### Notes on individual fields

`api.addr` — loopback by default. Inside a container it must be `0.0.0.0` for
Docker's port publishing to reach it; the image sets `LE_API_ADDR` accordingly,
and exposure is restricted by publishing as `-p 127.0.0.1:7777:7777`. The
supervisor warns at startup if it is bound non-loopback outside a container.

`api.shutdown_grace_seconds` — raise Docker's `stop_grace_period` to match, or
the container is killed partway through flushing.

`inference.start_timeout_seconds` — a cold GGUF can take minutes. Too short
looks like a crash loop.

`sandbox.mode` — `auto` selects the strongest available layer. `none` is the
container boundary only, and `le doctor` says so.

`offline` — refuses any non-local provider when the router is built, at
startup rather than at first call. It also refuses to coexist with
`egress.enabled`: offline mode has no route out, so the provisioning lanes
cannot exist. That is an error rather than a precedence rule, because silently
winning either way would leave you believing something untrue about the
machine.

`egress.enabled` — the §6.1 allowlisting proxy, **off by default**. It is the
only provisioned route out of the container, and a task sandbox is never
granted its port. Turning it on is what makes `le deps` and `le docs` work.

`egress.allowlist.rules` — each rule is a `host`, an optional `lanes` list, and
a **required** `why`. A host is either exact (`proxy.golang.org`) or carries one
leading wildcard label (`*.golang.org`, which covers subdomains but *not* the
apex). `*` alone is refused: an allowlist that allows everything should be an
absent proxy instead. Omitting `lanes` means every lane. Only ports 80 and 443
are reachable — widening that would turn a host allowlist into a general
tunnel, and it is deliberately not a config field.

`egress.deps_port` / `egress.docs_port` — loopback ports, one per lane. They
must differ: the lane comes from which socket the client reached, because a
lane carried in the request is a lane the client chooses.

### Environment overrides

`LE_API_ADDR`, `LE_PROFILE` and `LE_OFFLINE` override the file. The file wins
over the shipped defaults; the environment wins over the file.

## `providers.yaml`

```yaml
default: local
providers:
  - name: local
    kind: llamacpp              # llamacpp | openai_compatible | openai | anthropic
    base_url: http://127.0.0.1:8080
    model: your-model

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

roles:
  # Optional. Unlisted roles use `default`.
  verification: vllm
```

Roles: `orchestration`, `planning`, `coding`, `repository_search`,
`summarization`, `review`, `classification`, `reranking`, `verification`,
`embedding`.

The shipped state maps every role to one provider. Turn on multi-model routing
per role only when you have measured that it helps.

**Never put a secret here.** `api_key_env` names an environment variable:

```yaml
  - name: cloud
    kind: anthropic
    model: claude-opus-5
    api_key_env: ANTHROPIC_API_KEY
```

A provider whose variable is unset fails at startup, not at first call.

### Capabilities

| Field | What relies on it |
|---|---|
| `tool_calling` | Whether tool-driven steps route here |
| `structured_output` | `ChatStructured` returns `ErrUnsupported` if false — it never falls back to prompting for JSON |
| `grammar` | Grammar-constrained generation |
| `infill` | Fill-in-the-middle |
| `embeddings` | Semantic retrieval |
| `thinking_control` | Whether the thinking policy is sent |
| `max_context` | Packet sizing when no profile overrides it |
| `local` | Offline mode refuses `false` |

`llamacpp`, `openai` and `anthropic` declare their own. For
`openai_compatible`, declare them yourself — the conservative default is chat
only, because guessing wrong produces a confusing runtime failure.

## Profiles

Everything that depends on the machine:

```yaml
name: measured-8gb-gpu-64gb-ram
description: Generated by `le models bench` on 2026-09-14.
hardware:
  gpu: NVIDIA GeForce RTX 4060 Laptop GPU
  vram_mb: 8188
  ram_mb: 65536
  threads: 16
context_tokens: 32768
max_packet_tokens: 16384
reserved_output_tokens: 8192
max_tools_exposed: 12
measured_peak_vram_mb: 7104
measured_peak_ram_mb: 2210
max_concurrent_tasks: 1
runtime:
  threads: 8
  gpu_layers: 999
  flash_attn: true
  cache_type_k: q8_0
  extra_args: ["--n-cpu-moe", "999"]
sampling:
  temperature: 0.2
  top_p: 0.95
  top_k: 40
  min_p: 0.05
thinking_policy: auto        # off | auto | always
measured:
  model: your-model
  prefill_tokens_per_second: 412.6
  decode_tokens_per_second: 28.4
  measured_at: 2026-09-14T10:15:00Z
```

Validation enforces that `max_packet_tokens + reserved_output_tokens` does not
exceed `context_tokens`.

Shipped profiles are embedded in the binary, so they are available in every
install shape. A profile of the same name in `/data/config/profiles` overrides
the embedded one, so a measurement always beats a shipped starting point.

See [choose a hardware profile](../how-to/choose-a-profile.md).
