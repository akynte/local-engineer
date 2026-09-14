package critic_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/critic"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
)

// scripted returns a fixed structured answer and records what it was asked.
type scripted struct {
	caps     llm.Capabilities
	reply    string
	requests []llm.ChatRequest
}

func (s *scripted) Name() string                   { return "scripted" }
func (s *scripted) Capabilities() llm.Capabilities { return s.caps }
func (s *scripted) Health(context.Context) error   { return nil }
func (s *scripted) Close() error                   { return nil }
func (s *scripted) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: s.reply}, nil
}
func (s *scripted) ChatStructured(_ context.Context, req llm.ChatRequest, _ json.RawMessage) (*llm.ChatResponse, error) {
	s.requests = append(s.requests, req)
	return &llm.ChatResponse{Content: s.reply}, nil
}
func (s *scripted) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "scripted", Capability: "embeddings"}
}
func (s *scripted) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "scripted", Capability: "infill"}
}

func structured() llm.Capabilities {
	return llm.Capabilities{Kind: llm.KindLlamaCPP, StructuredOutput: true, Local: true}
}

// The whole point of a fresh-context review is that it does not have the
// conversation that produced the change. §10.1 names the failure as
// "self-consistent errors": the context that produced a mistake contains every
// reason the mistake looked right.
func TestReviewIsNotGivenTheConversation(t *testing.T) {
	p := &scripted{caps: structured(), reply: `{"summary":"adds a nil check","concerns":[]}`}
	c := &critic.Critic{Provider: p, MaxTokens: 512}

	_, err := c.Review(context.Background(), "fix the panic",
		"--- a/x.go\n+++ b/x.go\n+if u == nil { return ErrNotFound }\n",
		[]recipe.Result{{Recipe: "go test", Kind: recipe.KindTest, Status: recipe.Pass,
			Summary: recipe.Summary{Headline: "1 package(s) passed"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 1 {
		t.Fatalf("made %d calls, want 1", len(p.requests))
	}
	// Two turns only: the system prompt and the material. Any tool history
	// would be the context the technique exists to escape.
	if got := len(p.requests[0].Messages); got != 2 {
		t.Errorf("the review was given %d turns, want 2 (system and material)", got)
	}
	body := p.requests[0].Messages[1].Content
	for _, want := range []string{"fix the panic", "go test", "ErrNotFound"} {
		if !strings.Contains(body, want) {
			t.Errorf("the review was not given %q", want)
		}
	}
}

// A review reports concerns. It does not accept, reject, or return a verdict —
// the completion contract decides from evidence, and a model voting on its own
// work is what §10.1 rules out.
func TestReviewReturnsConcernsNotAVerdict(t *testing.T) {
	p := &scripted{caps: structured(), reply: `{"summary":"ok","concerns":[
		{"severity":"medium","detail":"the empty-slice case is untested","path":"x.go"}]}`}
	c := &critic.Critic{Provider: p, MaxTokens: 512}

	out, err := c.Review(context.Background(), "obj", "diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Concerns) != 1 || out.Concerns[0].Detail == "" {
		t.Fatalf("concerns not parsed: %+v", out)
	}
	// The type carries no accepted/rejected field at all, which is the
	// structural version of the rule.
	if out.Summary == "" {
		t.Error("no summary for the gate")
	}
}

// An empty concern list is a useful answer, and padding it is not.
func TestReviewAcceptsAnEmptyConcernList(t *testing.T) {
	p := &scripted{caps: structured(), reply: `{"summary":"looks right","concerns":[]}`}
	c := &critic.Critic{Provider: p, MaxTokens: 512}
	out, err := c.Review(context.Background(), "obj", "diff", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Concerns) != 0 {
		t.Errorf("got %d concerns from an empty list", len(out.Concerns))
	}
}

// DR-4: refuse rather than degrade. A review parsed out of prose would be one
// whose concerns are sometimes silently dropped.
func TestReviewRefusesWithoutStructuredOutput(t *testing.T) {
	p := &scripted{caps: llm.Capabilities{Kind: llm.KindOpenAICompatible}, reply: "{}"}
	c := &critic.Critic{Provider: p}
	_, err := c.Review(context.Background(), "obj", "diff", nil)
	var unsupported *llm.UnsupportedError
	if !errors.As(err, &unsupported) {
		t.Fatalf("got %v, want an UnsupportedError", err)
	}
}

// Diagnosis is given the failures and not the attempts: the reasoning that
// produced them is what keeps producing them.
func TestDiagnosisSeesFailuresNotAttempts(t *testing.T) {
	p := &scripted{caps: structured(), reply: `{"cause":"the test asserts the old signature",
		"suggestion":"update the call site too","ruled_out":["a nil map"]}`}
	c := &critic.Critic{Provider: p, MaxTokens: 512}

	h, err := c.Diagnose(context.Background(), "change the signature", []recipe.Result{{
		Recipe: "go build", Kind: recipe.KindBuild, Status: recipe.Fail,
		Summary: recipe.Summary{
			Headline: "1 error",
			Findings: []recipe.Finding{{File: "b.go", Line: 12, Message: "not enough arguments"}},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if h.Cause == "" || h.Suggestion == "" {
		t.Fatalf("diagnosis incomplete: %+v", h)
	}
	// What the evidence already rules out matters as much as the hypothesis:
	// §10.1 adopts persistent cross-attempt state so the next attempt does not
	// re-test them.
	if len(h.RuledOut) == 0 {
		t.Error("the diagnosis ruled nothing out; the next attempt will retread")
	}
	body := p.requests[0].Messages[1].Content
	if !strings.Contains(body, "not enough arguments") {
		t.Error("the diagnosis was not given the finding")
	}
}

func TestMalformedOutputIsAnErrorNotAnEmptyReview(t *testing.T) {
	p := &scripted{caps: structured(), reply: "I think it looks fine, honestly"}
	c := &critic.Critic{Provider: p, MaxTokens: 512}
	if _, err := c.Review(context.Background(), "obj", "diff", nil); err == nil {
		t.Fatal("prose was accepted as a structured review; concerns would be silently lost")
	}
}
