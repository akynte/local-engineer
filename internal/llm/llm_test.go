package llm_test

// These tests pin the contract DR-4 depends on: capabilities are declarations
// callers may rely on, and a gap is an error rather than a silent degradation.

import (
	"context"
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
