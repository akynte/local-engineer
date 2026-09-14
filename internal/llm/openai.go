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

// OpenAICompatible speaks the OpenAI-compatible chat, embeddings and infill
// HTTP surfaces (DR-4). It backs the llamacpp, openai_compatible and openai
// provider kinds, which differ only in what they declare and which endpoints
// they expose.
type OpenAICompatible struct {
	name    string
	baseURL string
	apiKey  string
	model   string
	caps    Capabilities
	client  *http.Client
}

// Options configures a provider instance.
type Options struct {
	Name    string
	BaseURL string
	APIKey  string
	Model   string
	Caps    Capabilities
	Timeout time.Duration
	// Transport is overridable so tests can avoid real sockets.
	Transport http.RoundTripper
}

// NewOpenAICompatible builds a provider against any OpenAI-compatible URL:
// vLLM, SGLang, Ollama, LM Studio, TGI or a cloud gateway (§9.1).
func NewOpenAICompatible(o Options) *OpenAICompatible {
	timeout := o.Timeout
	if timeout == 0 {
		// Local generation on an 8 GB GPU can legitimately take minutes for a
		// long packet; a short default would look like a provider fault.
		timeout = 10 * time.Minute
	}
	if o.Caps.Kind == "" {
		o.Caps.Kind = KindOpenAICompatible
	}
	return &OpenAICompatible{
		name: o.Name, baseURL: strings.TrimRight(o.BaseURL, "/"),
		apiKey: o.APIKey, model: o.Model, caps: o.Caps,
		client: &http.Client{Timeout: timeout, Transport: o.Transport},
	}
}

// NewLlamaCPP builds a provider for a llama-server, embedded or external.
// llama.cpp advertises grammars and infill, which is why it gets its own
// constructor rather than a generic one with hand-written capabilities.
func NewLlamaCPP(o Options) *OpenAICompatible {
	if o.Caps.Kind == "" {
		o.Caps = Capabilities{
			Kind: KindLlamaCPP, ToolCalling: true, StructuredOutput: true, Grammar: true,
			Infill: true, Embeddings: true, ThinkingControl: true, Local: true,
			MaxContext: o.Caps.MaxContext,
		}
	}
	return NewOpenAICompatible(o)
}

func (p *OpenAICompatible) Name() string               { return p.name }
func (p *OpenAICompatible) Capabilities() Capabilities { return p.caps }
func (p *OpenAICompatible) Close() error               { p.client.CloseIdleConnections(); return nil }

func (p *OpenAICompatible) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	return p.chat(ctx, req, nil)
}

// ChatStructured refuses when the provider does not declare structured output,
// as DR-4 requires.
func (p *OpenAICompatible) ChatStructured(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	if !p.caps.StructuredOutput {
		return nil, &UnsupportedError{Provider: p.name, Capability: "structured output"}
	}
	if len(schema) == 0 {
		return nil, fmt.Errorf("llm: ChatStructured called without a schema")
	}
	return p.chat(ctx, req, schema)
}

func (p *OpenAICompatible) chat(ctx context.Context, req ChatRequest, schema json.RawMessage) (*ChatResponse, error) {
	model := req.Model
	if model == "" {
		model = p.model
	}
	body := map[string]any{"model": model, "messages": req.Messages}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.TopK != nil {
		body["top_k"] = *req.TopK
	}
	if req.MinP != nil {
		body["min_p"] = *req.MinP
	}
	if req.MaxTokens > 0 {
		body["max_tokens"] = req.MaxTokens
	}
	if len(req.Stop) > 0 {
		body["stop"] = req.Stop
	}
	if req.Seed != nil {
		body["seed"] = *req.Seed
	}
	if schema != nil {
		body["response_format"] = map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name": "response", "strict": true, "schema": schema,
			},
		}
	}
	if req.Thinking != "" && p.caps.ThinkingControl {
		// llama.cpp and several gateways accept this; providers that do not
		// declare ThinkingControl never see the field.
		body["chat_template_kwargs"] = map[string]any{"enable_thinking": req.Thinking != "off"}
	}

	start := time.Now()
	var out struct {
		Model   string `json:"model"`
		Choices []struct {
			Message      Message `json:"message"`
			FinishReason string  `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			PromptDetails    struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
		// llama.cpp reports cache reuse here.
		TimingsCacheN int `json:"tokens_cached"`
	}
	if err := p.post(ctx, "/v1/chat/completions", body, &out); err != nil {
		return nil, err
	}
	if len(out.Choices) == 0 {
		return nil, fmt.Errorf("llm: %s returned no choices", p.name)
	}
	cached := out.Usage.PromptDetails.CachedTokens
	if cached == 0 {
		cached = out.TimingsCacheN
	}
	return &ChatResponse{
		Content: out.Choices[0].Message.Content, FinishReason: out.Choices[0].FinishReason,
		PromptTokens: out.Usage.PromptTokens, OutputTokens: out.Usage.CompletionTokens,
		CachedTokens: cached, Model: out.Model, DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

func (p *OpenAICompatible) Embed(ctx context.Context, req EmbedRequest) (*EmbedResponse, error) {
	if !p.caps.Embeddings {
		return nil, &UnsupportedError{Provider: p.name, Capability: "embeddings"}
	}
	model := req.Model
	if model == "" {
		model = p.model
	}
	var out struct {
		Model string `json:"model"`
		Data  []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := p.post(ctx, "/v1/embeddings", map[string]any{"model": model, "input": req.Input}, &out); err != nil {
		return nil, err
	}
	res := &EmbedResponse{Model: out.Model}
	for _, d := range out.Data {
		res.Vectors = append(res.Vectors, d.Embedding)
	}
	if len(res.Vectors) > 0 {
		res.Dims = len(res.Vectors[0])
	}
	return res, nil
}

func (p *OpenAICompatible) Infill(ctx context.Context, req InfillRequest) (*ChatResponse, error) {
	if !p.caps.Infill {
		return nil, &UnsupportedError{Provider: p.name, Capability: "infill"}
	}
	start := time.Now()
	body := map[string]any{"input_prefix": req.Prefix, "input_suffix": req.Suffix}
	if req.MaxTokens > 0 {
		body["n_predict"] = req.MaxTokens
	}
	var out struct {
		Content string `json:"content"`
		Timings struct {
			PredictedN int `json:"predicted_n"`
			PromptN    int `json:"prompt_n"`
		} `json:"timings"`
	}
	if err := p.post(ctx, "/infill", body, &out); err != nil {
		return nil, err
	}
	return &ChatResponse{
		Content: out.Content, FinishReason: "stop",
		PromptTokens: out.Timings.PromptN, OutputTokens: out.Timings.PredictedN,
		Model: p.model, DurationMS: time.Since(start).Milliseconds(),
	}, nil
}

// Health probes the server. /health is llama.cpp's; /v1/models is the
// OpenAI-compatible fallback that every other backend answers.
func (p *OpenAICompatible) Health(ctx context.Context) error {
	for _, path := range []string{"/health", "/v1/models"} {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+path, nil)
		if err != nil {
			return err
		}
		p.auth(req)
		resp, err := p.client.Do(req)
		if err != nil {
			continue
		}
		// Drain a bounded prefix so the connection can be reused; the body of
		// a health probe carries nothing we need.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return nil
		}
	}
	return fmt.Errorf("llm: provider %s at %s is not ready", p.name, p.baseURL)
}

func (p *OpenAICompatible) auth(req *http.Request) {
	if p.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+p.apiKey)
	}
}

func (p *OpenAICompatible) post(ctx context.Context, path string, payload, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	p.auth(req)

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("llm: %s %s: %w", p.name, path, err)
	}
	defer resp.Body.Close()

	// Cap the read: a misconfigured endpoint returning an HTML error page must
	// not be able to exhaust memory.
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("llm: %s %s: read body: %w", p.name, path, err)
	}
	if resp.StatusCode != http.StatusOK {
		snippet := string(raw)
		if len(snippet) > 512 {
			snippet = snippet[:512] + "…"
		}
		return fmt.Errorf("llm: %s %s: HTTP %d: %s", p.name, path, resp.StatusCode, strings.TrimSpace(snippet))
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("llm: %s %s: decode response: %w", p.name, path, err)
	}
	return nil
}
