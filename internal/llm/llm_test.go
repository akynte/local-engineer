package llm_test

// These tests pin the contract DR-4 depends on: capabilities are declarations
// callers may rely on, and a gap is an error rather than a silent degradation.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/llm"
)

func TestStructuredOutputIsRefusedWhenNotDeclared(t *testing.T) {
	p := llm.NewOpenAICompatible(llm.Options{
		Name: "plain", BaseURL: "http://127.0.0.1:1",
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, StructuredOutput: false},
	})
	_, err := p.ChatStructured(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hi"}},
	}, json.RawMessage(`{"type":"object"}`))
	if !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
	if !strings.Contains(err.Error(), "structured output") {
		t.Errorf("the error must name the missing capability: %v", err)
	}
}

func TestOfflineModeRefusesRemoteProviders(t *testing.T) {
	f := llm.ProvidersFile{
		Default: "cloud",
		Providers: []llm.ProviderSpec{{
			Name: "cloud", Kind: llm.KindOpenAI, Model: "x", APIKeyEnv: "LE_TEST_KEY",
		}},
	}
	t.Setenv("LE_TEST_KEY", "secret")

	if _, err := llm.NewRouter(f, true); err == nil {
		t.Fatal("offline mode must refuse a remote provider at construction")
	} else if !strings.Contains(err.Error(), "offline") {
		t.Errorf("the error must explain the offline conflict: %v", err)
	}
	if _, err := llm.NewRouter(f, false); err != nil {
		t.Fatalf("the same provider must work when offline is off: %v", err)
	}
}

func TestMissingAPIKeyEnvIsAStartupError(t *testing.T) {
	f := llm.ProvidersFile{
		Providers: []llm.ProviderSpec{{Name: "cloud", Kind: llm.KindOpenAI, APIKeyEnv: "LE_ABSENT_KEY"}},
	}
	if _, err := llm.NewRouter(f, false); err == nil {
		t.Fatal("a provider whose key env var is unset must fail at startup, not at first call")
	}
}

func TestDefaultRoutingSendsEveryRoleToOneProvider(t *testing.T) {
	f := llm.DefaultProvidersFile("http://127.0.0.1:8080", "local-model")
	r, err := llm.NewRouter(f, true)
	if err != nil {
		t.Fatal(err)
	}
	routing := r.Routing()
	if len(routing) != len(llm.AllRoles()) {
		t.Fatalf("routing must cover every role, got %d of %d", len(routing), len(llm.AllRoles()))
	}
	for role, name := range routing {
		if name != "local" {
			t.Errorf("role %s routed to %q; the shipped default is one model for all roles", role, name)
		}
	}
}

func TestUnknownRoleInProvidersFileIsRejected(t *testing.T) {
	f := llm.DefaultProvidersFile("http://127.0.0.1:8080", "m")
	f.Roles = map[string]string{"not_a_role": "local"}
	if _, err := llm.NewRouter(f, true); err == nil {
		t.Fatal("an unroutable role name must be rejected")
	}
}

// The Anthropic provider must put the system prompt in the top-level field,
// send max_tokens, and read usage from the documented field names.
func TestAnthropicWireShape(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("anthropic-version") == "" {
			t.Error("anthropic-version header is required")
		}
		if r.Header.Get("x-api-key") != "k" {
			t.Errorf("x-api-key = %q", r.Header.Get("x-api-key"))
		}
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"claude-opus-5","stop_reason":"end_turn",
			"content":[{"type":"thinking","text":"..."},{"type":"text","text":"hello"}],
			"usage":{"input_tokens":10,"output_tokens":3,"cache_read_input_tokens":7}}`))
	}))
	defer srv.Close()

	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: srv.URL, APIKey: "k", Model: "claude-opus-5"})
	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "system", Content: "be terse"},
			{Role: "user", Content: "hi"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got["system"] != "be terse" {
		t.Errorf("the system prompt must be a top-level field, got %v", got["system"])
	}
	msgs, _ := got["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("system must not appear in messages, got %d turns", len(msgs))
	}
	if got["max_tokens"] == nil {
		t.Error("max_tokens is required by this API and must always be sent")
	}
	if resp.Content != "hello" {
		t.Errorf("only text blocks form the answer, got %q", resp.Content)
	}
	if resp.CachedTokens != 7 {
		t.Errorf("cached tokens = %d, want 7", resp.CachedTokens)
	}
	if resp.PromptTokens != 17 {
		t.Errorf("prompt tokens must include cache reads: got %d, want 17", resp.PromptTokens)
	}
}

func TestAnthropicRefusalIsATypedOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"stop_reason":"refusal","stop_details":{"category":"cyber","explanation":"declined"},
			"content":[],"usage":{}}`))
	}))
	defer srv.Close()

	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: srv.URL, APIKey: "k"})
	_, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{{Role: "user", Content: "x"}}})
	var re *llm.RefusalError
	if !errors.As(err, &re) {
		t.Fatalf("a refusal must be a typed outcome, got %T: %v", err, err)
	}
	if re.Category != "cyber" {
		t.Errorf("category = %q", re.Category)
	}
}

func TestAnthropicRejectsAssistantPrefill(t *testing.T) {
	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	_, err := p.Chat(context.Background(), llm.ChatRequest{Messages: []llm.Message{
		{Role: "user", Content: "x"}, {Role: "assistant", Content: "{"},
	}})
	if err == nil || !strings.Contains(err.Error(), "prefill") {
		t.Fatalf("a trailing assistant turn must be caught locally with a useful message, got %v", err)
	}
}

func TestAnthropicDoesNotClaimEmbeddings(t *testing.T) {
	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: "http://127.0.0.1:1", APIKey: "k"})
	if p.Capabilities().Embeddings {
		t.Fatal("this API has no embeddings endpoint; the capability must not be declared")
	}
	if _, err := p.Embed(context.Background(), llm.EmbedRequest{}); !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

// Tool calling has two incompatible wire formats behind one interface. Both
// are pinned here, because a silent mismatch shows up as a model that never
// calls a tool — which looks like a bad model rather than a bad adapter.

var weatherTool = llm.ToolDef{
	Name: "get_weather", Description: "Look up the weather",
	Schema: json.RawMessage(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"],"additionalProperties":false}`),
}

func TestOpenAIToolWireFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"m","choices":[{"finish_reason":"tool_calls","message":{
			"role":"assistant","content":"",
			"tool_calls":[{"id":"call_1","type":"function",
			  "function":{"name":"get_weather","arguments":"{\"city\":\"Oslo\"}"}}]}}],
			"usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompatible(llm.Options{
		Name: "o", BaseURL: srv.URL, Model: "m",
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, ToolCalling: true},
	})
	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages:   []llm.Message{{Role: "user", Content: "weather in Oslo?"}},
		Tools:      []llm.ToolDef{weatherTool},
		ToolChoice: "auto",
	})
	if err != nil {
		t.Fatal(err)
	}

	tools, _ := got["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("tools not sent: %v", got["tools"])
	}
	first, _ := tools[0].(map[string]any)
	if first["type"] != "function" {
		t.Errorf("this surface wraps tools in a function envelope, got %v", first["type"])
	}
	fn, _ := first["function"].(map[string]any)
	if fn["name"] != "get_weather" || fn["parameters"] == nil {
		t.Errorf("function payload = %v", fn)
	}

	if !resp.WantsTools() || len(resp.ToolCalls) != 1 {
		t.Fatalf("tool calls not parsed: %+v", resp)
	}
	tc := resp.ToolCalls[0]
	if tc.ID != "call_1" || tc.Name != "get_weather" {
		t.Errorf("tool call = %+v", tc)
	}
	// Arguments stay raw: the caller validates them against the tool's schema
	// and reports a violation back as a tool result the model can act on.
	var args struct{ City string }
	if err := json.Unmarshal(tc.Arguments, &args); err != nil || args.City != "Oslo" {
		t.Errorf("arguments = %s (%v)", tc.Arguments, err)
	}
}

func TestOpenAIReplaysToolCallsAndResults(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"m","choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"18C"}}],"usage":{}}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompatible(llm.Options{
		Name: "o", BaseURL: srv.URL, Model: "m",
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, ToolCalling: true},
	})
	_, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "call_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
			{Role: "tool", ToolCallID: "call_1", Content: "18C"},
		},
		Tools: []llm.ToolDef{weatherTool},
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 turns, got %d", len(msgs))
	}
	assistant, _ := msgs[1].(map[string]any)
	calls, _ := assistant["tool_calls"].([]any)
	if len(calls) != 1 {
		t.Fatal("the assistant turn must replay its tool calls, or the provider rejects the result that follows")
	}
	toolMsg, _ := msgs[2].(map[string]any)
	if toolMsg["tool_call_id"] != "call_1" {
		t.Errorf("tool result must carry the call id, got %v", toolMsg)
	}
}

// The Messages API expresses the same thing with content blocks and a
// different schema key, and tool results are user turns rather than a role.
func TestAnthropicToolWireFormat(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"claude-opus-5","stop_reason":"tool_use","content":[
			{"type":"text","text":"checking"},
			{"type":"tool_use","id":"toolu_1","name":"get_weather","input":{"city":"Oslo"}}],
			"usage":{"input_tokens":5,"output_tokens":2}}`))
	}))
	defer srv.Close()

	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: srv.URL, APIKey: "k",
		Caps: llm.Capabilities{ToolCalling: true}})
	resp, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "weather?"}},
		Tools:    []llm.ToolDef{weatherTool},
	})
	if err != nil {
		t.Fatal(err)
	}

	tools, _ := got["tools"].([]any)
	first, _ := tools[0].(map[string]any)
	if first["input_schema"] == nil {
		t.Errorf("this API names the schema input_schema, got keys %v", keysOf(first))
	}
	if first["function"] != nil {
		t.Error("this API does not use a function envelope")
	}

	if len(resp.ToolCalls) != 1 || resp.ToolCalls[0].ID != "toolu_1" {
		t.Fatalf("tool use blocks not parsed: %+v", resp.ToolCalls)
	}
	if resp.Content != "checking" {
		t.Errorf("text blocks alongside a tool use must still form the content, got %q", resp.Content)
	}
}

func TestAnthropicToolResultsBecomeUserBlocks(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&got)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"model":"m","stop_reason":"end_turn","content":[{"type":"text","text":"ok"}],"usage":{}}`))
	}))
	defer srv.Close()

	p := llm.NewAnthropic(llm.Options{Name: "a", BaseURL: srv.URL, APIKey: "k",
		Caps: llm.Capabilities{ToolCalling: true}})
	_, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{
			{Role: "user", Content: "weather?"},
			{Role: "assistant", ToolCalls: []llm.ToolCall{
				{ID: "toolu_1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Oslo"}`)}}},
			{Role: "tool", ToolCallID: "toolu_1", Content: "18C"},
			{Role: "tool", ToolCallID: "toolu_2", Content: "sunny"},
		},
		Tools: []llm.ToolDef{weatherTool},
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 3 {
		t.Fatalf("two tool results must merge into one user turn; got %d turns", len(msgs))
	}
	last, _ := msgs[2].(map[string]any)
	if last["role"] != "user" {
		t.Errorf("a tool result is a user turn here, got role %v", last["role"])
	}
	blocks, _ := last["content"].([]any)
	if len(blocks) != 2 {
		t.Fatalf("expected both results in one turn, got %d blocks", len(blocks))
	}
	b0, _ := blocks[0].(map[string]any)
	if b0["type"] != "tool_result" || b0["tool_use_id"] != "toolu_1" {
		t.Errorf("block = %v", b0)
	}
}

func TestToolsAgainstAProviderThatDoesNotDeclareThemIsRefused(t *testing.T) {
	p := llm.NewOpenAICompatible(llm.Options{
		Name: "plain", BaseURL: "http://127.0.0.1:1",
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, ToolCalling: false},
	})
	_, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "x"}},
		Tools:    []llm.ToolDef{weatherTool},
	})
	if !errors.Is(err, llm.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported, got %v", err)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// A provider that does not declare Vision must refuse an image, not drop it.
// A dropped image is the worst shape of failure available here: the request
// succeeds, the model answers confidently, and the answer is about nothing.
func TestImagesAreRefusedWhenVisionIsNotDeclared(t *testing.T) {
	var reached bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a cat"}}]}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompatible(llm.Options{
		Name: "blind", BaseURL: srv.URL,
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, Vision: false},
	})
	_, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{
			Role: "user", Content: "What is in this picture?",
			Images: []llm.Image{{MediaType: "image/png", Data: []byte{0x89, 'P', 'N', 'G'}}},
		}},
	})
	var unsupported *llm.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("got %v, want an UnsupportedError for vision", err)
	}
	if reached {
		t.Error("the request was sent to the provider despite the image being unsupported")
	}
}

// A provider that declares Vision encodes the image as a data URI in the
// content-array form, with the text first.
func TestImagesAreEncodedAsDataURIs(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"a cat"}}]}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompatible(llm.Options{
		Name: "seeing", BaseURL: srv.URL,
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, Vision: true},
	})
	if _, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{
			Role: "user", Content: "What is in this picture?",
			Images: []llm.Image{{MediaType: "image/png", Data: []byte("PNGDATA")}},
		}},
	}); err != nil {
		t.Fatal(err)
	}

	msgs, ok := body["messages"].([]any)
	if !ok || len(msgs) == 0 {
		t.Fatalf("no messages in the request body: %v", body)
	}
	content, ok := msgs[0].(map[string]any)["content"].([]any)
	if !ok {
		t.Fatalf("a message with an image did not use the content-array form: %v", msgs[0])
	}
	if len(content) != 2 {
		t.Fatalf("got %d content parts, want text then image", len(content))
	}
	if content[0].(map[string]any)["type"] != "text" {
		t.Error("the text part is not first; several servers ignore a trailing instruction")
	}
	img := content[1].(map[string]any)
	if img["type"] != "image_url" {
		t.Fatalf("second part is %v, want image_url", img["type"])
	}
	url := img["image_url"].(map[string]any)["url"].(string)
	want := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	if url != want {
		t.Errorf("data URI = %q, want %q", url, want)
	}
}

// A turn with no images keeps the plain string form, so nothing changes for
// every existing caller.
func TestMessagesWithoutImagesKeepTheStringForm(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer srv.Close()

	p := llm.NewOpenAICompatible(llm.Options{
		Name: "plain", BaseURL: srv.URL,
		Caps: llm.Capabilities{Kind: llm.KindOpenAICompatible, Vision: true},
	})
	if _, err := p.Chat(context.Background(), llm.ChatRequest{
		Messages: []llm.Message{{Role: "user", Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	msgs := body["messages"].([]any)
	if _, isString := msgs[0].(map[string]any)["content"].(string); !isString {
		t.Errorf("a message with no images did not keep the string content form: %v", msgs[0])
	}
}
