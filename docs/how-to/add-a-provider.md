# Add a provider

A provider is anything that can answer a completion request. Adding one is a
single Go file plus a capability declaration.

## First: do you need one?

Probably not. If your backend speaks the OpenAI-compatible surface — vLLM,
SGLang, Ollama, LM Studio, TGI, most gateways — it is already supported as
configuration:

```yaml
providers:
  - name: my-backend
    kind: openai_compatible
    base_url: http://127.0.0.1:8000
    model: some-model
    capabilities:
      tool_calling: true
      structured_output: true
      max_context: 32768
      local: true
```

Write code only when the wire format genuinely differs.

## The interface

```go
type Provider interface {
    Name() string
    Capabilities() Capabilities
    Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
    ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error)
    Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
    Infill(ctx context.Context, req InfillRequest) (*ChatResponse, error)
    Health(ctx context.Context) error
    Close() error
}
```

## Declare capabilities honestly

This is the part that matters. `Capabilities` is a set of promises callers are
allowed to rely on, and the supervisor acts on them:

- `ChatStructured` against a provider that does not declare
  `StructuredOutput` returns a typed `UnsupportedError` naming the capability.
  It does **not** fall back to asking for JSON in the prompt.
- Offline mode refuses any provider whose `Local` is false, at startup.
- Roles are only routed to providers that can serve them.

Declaring a capability you do not have produces a confusing runtime failure
instead of a clear configuration error. When unsure, declare `false`.

A capability the API genuinely lacks returns `ErrUnsupported`:

```go
func (p *MyProvider) Embed(context.Context, EmbedRequest) (*EmbedResponse, error) {
    return nil, &UnsupportedError{Provider: p.name, Capability: "embeddings"}
}
```

## Worked example

`internal/llm/anthropic.go` is a complete native provider for a vendor whose
wire format is not OpenAI-compatible. It shows the shape:

- the system prompt is lifted out of `messages` into a top-level field;
- a required `max_tokens` is defaulted rather than omitted;
- a trailing assistant turn is rejected locally with a message saying what to
  use instead;
- structured output uses the vendor's own parameter;
- a policy decline is surfaced as a typed `RefusalError`, not as a transport
  error, because it is a normal non-retryable outcome;
- embeddings and infill return `ErrUnsupported`, because that API has neither.

## Register it

In `internal/llm/router.go`, add the kind:

```go
case KindMyVendor:
    return NewMyProvider(opts), nil
```

## Test it

Follow `internal/llm/llm_test.go`: an `httptest` server asserting the request
shape, and tests that the capability contract holds. Do not write a test that
needs a real API key.

## Use it

```yaml
providers:
  - name: mine
    kind: myvendor
    model: some-model
    api_key_env: MY_VENDOR_KEY
roles:
  review: mine
```

```console
$ le models health
$ le config show | jq .routing
```

Never put a secret in `providers.yaml`. `api_key_env` names an environment
variable; a provider whose variable is unset fails at startup.
