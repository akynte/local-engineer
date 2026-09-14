// Package llm is the provider boundary of design v3 §9.1 and DR-4.
//
// DR-4 chooses OpenAI-compatible HTTP plus an explicit Capabilities
// declaration. The disadvantage it records is the one this file is built
// around: "feature gaps between providers (thinking control, infill, grammars)
// are hidden behind one API and must be declared explicitly." So Capabilities
// is not advisory metadata — callers must consult it, and ChatStructured
// refuses rather than silently degrading when a provider cannot constrain
// output.
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// Role names a job in the system. §9.1 routes roles to providers in
// providers.yaml, with a default that maps all roles to one model.
type Role string

const (
	RoleOrchestration Role = "orchestration"
	RolePlanning      Role = "planning"
	RoleCoding        Role = "coding"
	RoleRepoSearch    Role = "repository_search"
	RoleSummarization Role = "summarization"
	RoleReview        Role = "review"
	RoleClassify      Role = "classification"
	RoleRerank        Role = "reranking"
	RoleVerification  Role = "verification"
	RoleEmbedding     Role = "embedding"
)

// AllRoles lists every routable role.
func AllRoles() []Role {
	return []Role{RoleOrchestration, RolePlanning, RoleCoding, RoleRepoSearch,
		RoleSummarization, RoleReview, RoleClassify, RoleRerank, RoleVerification, RoleEmbedding}
}

// Kind enumerates the provider kinds supported at 1.0 (§9.1).
type Kind string

const (
	KindLlamaCPP         Kind = "llamacpp"
	KindOpenAICompatible Kind = "openai_compatible"
	KindAnthropic        Kind = "anthropic"
	KindOpenAI           Kind = "openai"
)

// Capabilities is what a provider declares about itself. Every field is a
// promise the caller is allowed to rely on; a provider that is unsure must
// declare false.
type Capabilities struct {
	Kind Kind `json:"kind"`
	// ToolCalling: the provider implements OpenAI-style tool calls.
	ToolCalling bool `json:"tool_calling"`
	// StructuredOutput: the provider can constrain output to a JSON Schema
	// (llama.cpp grammars, or a native response_format).
	StructuredOutput bool `json:"structured_output"`
	// Grammar: the provider accepts a GBNF grammar directly.
	Grammar bool `json:"grammar"`
	// Infill: the provider exposes a fill-in-the-middle endpoint.
	Infill bool `json:"infill"`
	// Embeddings: the provider can embed.
	Embeddings bool `json:"embeddings"`
	// Vision: the provider accepts images.
	Vision bool `json:"vision"`
	// ThinkingControl: the caller can turn reasoning on or off.
	ThinkingControl bool `json:"thinking_control"`
	// MaxContext is the model's window in tokens, 0 when unknown.
	MaxContext int `json:"max_context"`
	// Local reports whether the provider runs on this machine. Offline mode
	// refuses to route any role to a non-local provider.
	Local bool `json:"local"`
	// CostPerMTokIn and Out are informational; local providers declare 0.
	CostPerMTokIn  float64 `json:"cost_per_mtok_in"`
	CostPerMTokOut float64 `json:"cost_per_mtok_out"`
}

// Message is one chat turn.
type Message struct {
	Role    string `json:"role"` // system | user | assistant | tool
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
	// ToolCallID links a tool result to the call that produced it.
	ToolCallID string `json:"tool_call_id,omitempty"`
	// ToolCalls are the calls an assistant turn requested. They must be
	// replayed verbatim on the next request: a provider that receives a tool
	// result without the call that produced it rejects the conversation.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ToolDef declares a tool the model may call.
type ToolDef struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	// Schema is the JSON Schema of the tool's parameters. A tool whose schema
	// does not constrain its arguments produces malformed calls from a small
	// model far more often than from a large one, so every tool here declares
	// required fields and forbids extras.
	Schema json.RawMessage `json:"schema"`
}

// ToolCall is one requested invocation.
type ToolCall struct {
	// ID correlates the call with its result. Providers generate it.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Arguments is the raw JSON the model produced. It is deliberately not
	// decoded here: the caller validates it against the tool's own schema and
	// reports a violation back to the model as a tool result, which is a
	// correction the model can act on.
	Arguments json.RawMessage `json:"arguments"`
}

// ChatRequest is a completion request.
type ChatRequest struct {
	Model       string    `json:"model,omitempty"`
	Messages    []Message `json:"messages"`
	Temperature *float64  `json:"temperature,omitempty"`
	TopP        *float64  `json:"top_p,omitempty"`
	TopK        *int      `json:"top_k,omitempty"`
	MinP        *float64  `json:"min_p,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Stop        []string  `json:"stop,omitempty"`
	Seed        *int      `json:"seed,omitempty"`
	// Thinking is honoured only when Capabilities().ThinkingControl is true.
	Thinking string `json:"-"` // off | auto | always
	// CachePrefixHint marks how many leading messages form the stable prefix,
	// so a cache-aware layout can be preserved (§8.2: stable prefix first).
	CachePrefixHint int `json:"-"`
	// Tools the model may call. Providers that do not declare ToolCalling
	// reject a request carrying them rather than silently dropping them.
	Tools []ToolDef `json:"-"`
	// ToolChoice is "auto" or "none". Forced tool choice is deliberately not
	// offered: several current models reject it, and an instruction in the
	// prompt achieves the same thing portably.
	ToolChoice string `json:"-"`
}

// ChatResponse is a completion result plus the accounting the telemetry and
// the dashboard need.
type ChatResponse struct {
	Content      string `json:"content"`
	FinishReason string `json:"finish_reason"`
	PromptTokens int    `json:"prompt_tokens"`
	OutputTokens int    `json:"output_tokens"`
	// CachedTokens is how many prompt tokens the provider served from its
	// prompt cache. §8.2 treats prompt-cache reuse as a first-class metric.
	CachedTokens int    `json:"cached_tokens"`
	Model        string `json:"model"`
	// DurationMS is wall-clock time for the call.
	DurationMS int64 `json:"duration_ms"`
	// ToolCalls the model requested. FinishReason names the tool stop when
	// this is non-empty.
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// WantsTools reports whether the model asked to call something.
func (r *ChatResponse) WantsTools() bool { return r != nil && len(r.ToolCalls) > 0 }

// EmbedRequest asks for embeddings.
type EmbedRequest struct {
	Model string   `json:"model,omitempty"`
	Input []string `json:"input"`
}

// EmbedResponse carries the vectors.
type EmbedResponse struct {
	Vectors [][]float32 `json:"vectors"`
	Dims    int         `json:"dims"`
	Model   string      `json:"model"`
}

// InfillRequest is a fill-in-the-middle request.
type InfillRequest struct {
	Model     string `json:"model,omitempty"`
	Prefix    string `json:"prefix"`
	Suffix    string `json:"suffix"`
	MaxTokens int    `json:"max_tokens,omitempty"`
}

// ErrUnsupported is returned when a caller asks for a capability the provider
// does not declare. It is a hard error on purpose: DR-4's disadvantage is that
// feature gaps hide behind one API, and silently degrading would realise
// exactly that risk.
var ErrUnsupported = errors.New("llm: capability not supported by this provider")

// UnsupportedError names the missing capability.
type UnsupportedError struct {
	Provider   string
	Capability string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("llm: provider %q does not support %s; "+
		"route this role to a provider that declares it, or use the deterministic fallback",
		e.Provider, e.Capability)
}

func (e *UnsupportedError) Is(target error) bool { return target == ErrUnsupported }

// Provider is the §9.1 interface. Adding a provider is one Go file
// implementing this plus a Capabilities declaration (§9.1).
type Provider interface {
	// Name identifies the provider instance in configuration and telemetry.
	Name() string
	// Capabilities declares what this provider can actually do.
	Capabilities() Capabilities
	// Chat is an unconstrained completion.
	Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error)
	// ChatStructured constrains output to a JSON Schema. It must return an
	// UnsupportedError when Capabilities().StructuredOutput is false rather
	// than falling back to prompt-only instructions.
	ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error)
	// Embed produces vectors.
	Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error)
	// Infill is fill-in-the-middle.
	Infill(ctx context.Context, req InfillRequest) (*ChatResponse, error)
	// Health reports whether the provider is reachable and ready.
	Health(ctx context.Context) error
	// Close releases any client resources.
	Close() error
}
