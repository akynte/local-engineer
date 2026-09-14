package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Anthropic implements Provider against the Anthropic Messages API.
//
// It speaks raw HTTP rather than the official SDK on purpose. DR-4 makes
// OpenAI-compatible HTTP the provider boundary and names "a native provider
// can implement it directly" as the replacement path; this provider is that
// path for one vendor whose wire format differs. Pulling the vendor SDK into a
// local-first tool would add a dependency tree to every build and every SBOM
// for a remote lane that is opt-in and disabled in offline mode (§9.1).
//
// The wire format implemented here:
//   - POST {base}/v1/messages
//   - headers: x-api-key, anthropic-version, content-type
//   - the system prompt is a top-level field, never a message
//   - max_tokens is required
//   - structured output is output_config.format with a json_schema
//   - a policy decline arrives as HTTP 200 with stop_reason "refusal"
type Anthropic struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	caps    Capabilities
	client  *http.Client
}

// AnthropicVersion is the required API version header value.
const AnthropicVersion = "2023-06-01"

// DefaultAnthropicModel is used when neither the request nor the provider spec
// names a model.
const DefaultAnthropicModel = "claude-opus-5"

// DefaultAnthropicMaxTokens is sent when a caller does not set MaxTokens.
// max_tokens is required by this API, and a low value truncates mid-thought.
const DefaultAnthropicMaxTokens = 16000

// NewAnthropic builds an Anthropic provider.
func NewAnthropic(o Options) *Anthropic {
	timeout := o.Timeout
	if timeout == 0 {
		timeout = 10 * time.Minute
	}
	base := o.BaseURL
	if base == "" {
		base = "https://api.anthropic.com"
	}
	model := o.Model
	if model == "" {
		model = DefaultAnthropicModel
	}
	caps := o.Caps
	caps.Kind = KindAnthropic
	caps.Local = false
	return &Anthropic{
		name: o.Name, baseURL: strings.TrimRight(base, "/"), apiKey: o.APIKey, model: model,
		caps: caps, client: &http.Client{Timeout: timeout, Transport: o.Transport},
	}
}

func (p *Anthropic) Name() string               { return p.name }
func (p *Anthropic) Capabilities() Capabilities { return p.caps }
func (p *Anthropic) Close() error               { p.client.CloseIdleConnections(); return nil }

// Embed and Infill are not offered by this API. Declaring the gap is DR-4's
// requirement; silently emulating it would hide the difference.
func (p *Anthropic) Embed(context.Context, EmbedRequest) (*EmbedResponse, error) {
	return nil, &UnsupportedError{Provider: p.name, Capability: "embeddings"}
}

func (p *Anthropic) Infill(context.Context, InfillRequest) (*ChatResponse, error) {
	return nil, &UnsupportedError{Provider: p.name, Capability: "infill"}
}

func (p *Anthropic) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return p.messages(ctx, req, nil)
}

func (p *Anthropic) ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	if !p.caps.StructuredOutput {
		return nil, &UnsupportedError{Provider: p.name, Capability: "structured output"}
	}
	if len(schema) == 0 {
		return nil, fmt.Errorf("llm: ChatStructured called without a schema")
	}
	return p.messages(ctx, req, schema)
}

// RefusalError reports a policy decline. It is a distinct type because a
// refusal is a normal, non-retryable outcome that the caller must surface to
// the operator rather than treat as a transport fault.
type RefusalError struct {
	Provider    string
	Category    string
	Explanation string
}

func (e *RefusalError) Error() string {
	return fmt.Sprintf("llm: %s declined the request (category %q): %s", e.Provider, e.Category, e.Explanation)
}

func (p *Anthropic) messages(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = DefaultAnthropicMaxTokens
	}

	// The system prompt is a top-level field here, not a message. Splitting it
	// out is required, not stylistic: a "system" role inside messages is
	// rejected.
	var system []string
	turns := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		if m.Role == "system" {
			system = append(system, m.Content)
			continue
		}
		turns = append(turns, map[string]any{"role": m.Role, "content": m.Content})
	}
	if len(turns) == 0 {
		return nil, fmt.Errorf("llm: %s: no user turns in request", p.name)
	}
	// Assistant prefill is rejected by current models; catch it here with a
	// message that says what to do instead.
	if turns[len(turns)-1]["role"] == "assistant" {
		return nil, fmt.Errorf("llm: %s: the request ends on an assistant turn (prefill), which current models reject; "+
			"use ChatStructured to constrain the output format instead", p.name)
	}

	body := map[string]any{"model": model, "max_tokens": maxTokens, "messages": turns}
	if len(system) > 0 {
		body["system"] = strings.Join(system, "\n\n")
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		body["stop_sequences"] = req.Stop
	}
	if schema != nil {
		body["output_config"] = map[string]any{
			"format": map[string]any{"type": "json_schema", "schema": schema},
		}
	}
	if p.caps.ThinkingControl {
		switch req.Thinking {
		case "off":
			body["thinking"] = map[string]any{"type": "disabled"}
		case "auto", "always":
			body["thinking"] = map[string]any{"type": "adaptive"}
		}
	}

	start := time.Now()
	var out struct {
		Model   string `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason  string `json:"stop_reason"`
		StopDetails *struct {
			Category    string `json:"category"`
			Explanation string `json:"explanation"`
		} `json:"stop_details"`
		Usage struct {
			InputTokens         int `json:"input_tokens"`
			OutputTokens        int `json:"output_tokens"`
			CacheReadTokens     int `json:"cache_read_input_tokens"`
			CacheCreationTokens int `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	}
	if err := p.post(ctx, "/v1/messages", body, &out); err != nil {
		return nil, err
	}

	if out.StopReason == "refusal" {
		e := &RefusalError{Provider: p.name}
		if out.StopDetails != nil {
			e.Category, e.Explanation = out.StopDetails.Category, out.StopDetails.Explanation
		}
		return nil, e
	}

	// Only text blocks carry the answer; thinking blocks are not the response.
	var sb strings.Builder
	for _, c := range out.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return &ChatResponse{
		Content: sb.String(), FinishReason: out.StopReason,
		PromptTokens: out.Usage.InputTokens + out.Usage.CacheReadTokens + out.Usage.CacheCreationTokens,
		OutputTokens: out.Usage.OutputTokens, CachedTokens: out.Usage.CacheReadTokens,
		Model: out.Model, DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// Health sends the smallest valid request. This API has no unauthenticated
// health endpoint, so readiness and credentials are checked together.
func (p *Anthropic) Health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"/v1/models", nil)
	if err != nil {
		return err
	}
	p.headers(req)
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("llm: %s health: %w", p.name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("llm: %s health: HTTP %d", p.name, resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out)
}

func (p *Anthropic) headers(req *http.Request) {
	req.Header.Set("x-api-key", p.apiKey)
	req.Header.Set("anthropic-version", AnthropicVersion)
	req.Header.Set("content-type", "application/json")
}

func (p *Anthropic) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	p.headers(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("llm: %s %s: %w", p.name, path, err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("llm: %s %s: read body: %w", p.name, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := strings.TrimSpace(string(raw))
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		return fmt.Errorf("llm: %s %s: HTTP %d: %s", p.name, path, resp.StatusCode, snippet)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("llm: %s %s: decode response: %w", p.name, path, err)
	}
	return nil
}
