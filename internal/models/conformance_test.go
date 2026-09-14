package models

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/akynte/local-engineer/internal/llm"
)

// liar declares capabilities it does not have, which is the fault this checker
// exists to catch. Declaring is cheap and nothing verified it, so a providers.
// yaml entry could promise tool calling for a model that cannot do it and the
// failure would surface inside a task as the model being poor at its job.
type liar struct {
	caps llm.Capabilities
	// toolResponse is what Chat returns when tools were offered.
	toolResponse *llm.ChatResponse
	// structuredResponse is what ChatStructured returns.
	structuredResponse *llm.ChatResponse
	// degrade makes ChatStructured answer instead of refusing, which DR-4
	// forbids for a provider that does not declare the capability.
	degrade bool
}

func (l *liar) Name() string                   { return "liar" }
func (l *liar) Capabilities() llm.Capabilities { return l.caps }
func (l *liar) Health(context.Context) error   { return nil }
func (l *liar) Close() error                   { return nil }

func (l *liar) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if len(req.Tools) > 0 && l.toolResponse != nil {
		return l.toolResponse, nil
	}
	return &llm.ChatResponse{Content: "blue", OutputTokens: 2, FinishReason: "stop"}, nil
}

func (l *liar) ChatStructured(_ context.Context, _ llm.ChatRequest, _ json.RawMessage) (*llm.ChatResponse, error) {
	if !l.caps.StructuredOutput && !l.degrade {
		return nil, &llm.UnsupportedError{Provider: "liar", Capability: "structured output"}
	}
	if l.structuredResponse != nil {
		return l.structuredResponse, nil
	}
	return &llm.ChatResponse{Content: `{"language":"Go","confident":true}`, OutputTokens: 9}, nil
}

func (l *liar) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	if !l.caps.Embeddings {
		return nil, &llm.UnsupportedError{Provider: "liar", Capability: "embeddings"}
	}
	return &llm.EmbedResponse{Vectors: [][]float32{{1, 2}, {3, 4}}, Dims: 2}, nil
}

func (l *liar) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "liar", Capability: "infill"}
}

func checkNamed(t *testing.T, res Conformance, name string) Check {
	t.Helper()
	for _, c := range res.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q in %+v", name, res.Checks)
	return Check{}
}

func honest() llm.Capabilities {
	return llm.Capabilities{
		Kind: llm.KindLlamaCPP, ToolCalling: true, StructuredOutput: true,
		Embeddings: true, Local: true,
	}
}

// A provider that declares tool calling and answers in prose has not merely
// performed badly: it has broken a promise the engine relies on.
func TestDeclaredToolCallingThatDoesNotCallIsAFailure(t *testing.T) {
	p := &liar{caps: honest(), toolResponse: &llm.ChatResponse{
		Content: "It is 18 degrees in Berlin.", FinishReason: "stop", OutputTokens: 8,
	}}
	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	c := checkNamed(t, res, "tool calling")
	if c.Status != StatusFail {
		t.Errorf("tool calling = %q, want fail: the provider declared it and did not call", c.Status)
	}
	if res.Passed {
		t.Error("the report passed despite a broken declaration")
	}
}

// Malformed arguments are the failure mode that actually bites: the call
// arrives, the engine tries to use it, and the schema was never satisfied.
func TestToolArgumentsMustSatisfyTheSchema(t *testing.T) {
	p := &liar{caps: honest(), toolResponse: &llm.ChatResponse{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID: "1", Name: "get_weather", Arguments: json.RawMessage(`{"city":"Berlin"}`),
		}},
	}}
	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	c := checkNamed(t, res, "tool calling")
	if c.Status != StatusFail {
		t.Errorf("tool calling = %q, want fail: the required field `units` is missing", c.Status)
	}
}

func TestWellFormedToolCallPasses(t *testing.T) {
	p := &liar{caps: honest(), toolResponse: &llm.ChatResponse{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID: "1", Name: "get_weather",
			Arguments: json.RawMessage(`{"city":"Berlin","units":"celsius"}`),
		}},
	}}
	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	if c := checkNamed(t, res, "tool calling"); c.Status != StatusPass {
		t.Errorf("tool calling = %q (%s), want pass", c.Status, c.Detail)
	}
}

// DR-4: a provider that cannot constrain output must refuse, not hand back
// prose the caller will try to parse.
func TestSilentDegradationIsAFailure(t *testing.T) {
	caps := honest()
	caps.StructuredOutput = false
	p := &liar{caps: caps, degrade: true}

	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	c := checkNamed(t, res, "refuses undeclared")
	if c.Status != StatusFail {
		t.Errorf("refusal check = %q, want fail: the provider answered instead of refusing", c.Status)
	}
}

func TestProperRefusalPasses(t *testing.T) {
	caps := honest()
	caps.StructuredOutput = false
	p := &liar{caps: caps}

	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	if c := checkNamed(t, res, "refuses undeclared"); c.Status != StatusPass {
		t.Errorf("refusal check = %q (%s), want pass", c.Status, c.Detail)
	}
}

// An undeclared capability is skipped, never failed. A provider is allowed to
// be limited; it is not allowed to be wrong about itself.
func TestUndeclaredCapabilitiesAreSkippedNotFailed(t *testing.T) {
	caps := llm.Capabilities{Kind: llm.KindOpenAICompatible, Local: true}
	res := CheckConformance(context.Background(), &liar{caps: caps}, ConformanceOptions{})

	for _, name := range []string{"tool calling", "embeddings"} {
		if c := checkNamed(t, res, name); c.Status != StatusSkip {
			t.Errorf("%s = %q, want skip for an undeclared capability", name, c.Status)
		}
	}
	if !res.Passed {
		t.Error("a provider that declared little and delivered it was marked failing")
	}
}

// A response cut off at the output limit is not a pass. It is the reasoning
// model failure mode, and calling it a pass would hide it.
func TestEmptyCompletionIsUnprovenNotPass(t *testing.T) {
	p := &emptyTalker{caps: honest()}
	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	c := checkNamed(t, res, "completion")
	if c.Status != StatusUnproven {
		t.Errorf("completion = %q, want unproven for an empty answer", c.Status)
	}
	if c.Status == StatusPass {
		t.Error("an empty completion was reported as a pass")
	}
}

type emptyTalker struct{ caps llm.Capabilities }

func (e *emptyTalker) Name() string                   { return "empty" }
func (e *emptyTalker) Capabilities() llm.Capabilities { return e.caps }
func (e *emptyTalker) Health(context.Context) error   { return nil }
func (e *emptyTalker) Close() error                   { return nil }
func (e *emptyTalker) Chat(context.Context, llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: "", FinishReason: "length", OutputTokens: 2048}, nil
}
func (e *emptyTalker) ChatStructured(context.Context, llm.ChatRequest, json.RawMessage) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: `{"language":"Go","confident":true}`}, nil
}
func (e *emptyTalker) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return &llm.EmbedResponse{Vectors: [][]float32{{1}, {2}}, Dims: 1}, nil
}
func (e *emptyTalker) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "empty", Capability: "infill"}
}

// A provider that ignores max_tokens overruns every packet budget the profile
// computes from it.
func TestIgnoredOutputLimitIsAFailure(t *testing.T) {
	p := &overrunner{caps: honest()}
	res := CheckConformance(context.Background(), p, ConformanceOptions{})
	if c := checkNamed(t, res, "max tokens"); c.Status != StatusFail {
		t.Errorf("max tokens = %q, want fail: the provider returned far more than asked", c.Status)
	}
}

type overrunner struct{ caps llm.Capabilities }

func (o *overrunner) Name() string                   { return "overrun" }
func (o *overrunner) Capabilities() llm.Capabilities { return o.caps }
func (o *overrunner) Health(context.Context) error   { return nil }
func (o *overrunner) Close() error                   { return nil }
func (o *overrunner) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: "one two three", OutputTokens: req.MaxTokens * 40, FinishReason: "stop"}, nil
}
func (o *overrunner) ChatStructured(context.Context, llm.ChatRequest, json.RawMessage) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: `{"language":"Go","confident":true}`}, nil
}
func (o *overrunner) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return &llm.EmbedResponse{Vectors: [][]float32{{1}, {2}}, Dims: 1}, nil
}
func (o *overrunner) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "overrun", Capability: "infill"}
}

// A provider that quietly accepts an image it never declared it can read is
// reported, not failed: from outside there is no way to tell whether it read
// the image or dropped it, and failing would assert the worse of the two.
func TestUndeclaredVisionThatAcceptsImagesIsReported(t *testing.T) {
	caps := honest()
	caps.Vision = false
	res := CheckConformance(context.Background(), &liar{caps: caps, toolResponse: &llm.ChatResponse{
		FinishReason: "tool_calls",
		ToolCalls: []llm.ToolCall{{
			ID: "1", Name: "get_weather",
			Arguments: json.RawMessage(`{"city":"Berlin","units":"celsius"}`),
		}},
	}}, ConformanceOptions{})

	c := checkNamed(t, res, "vision")
	if c.Status != StatusUnproven {
		t.Errorf("vision = %q, want unproven for a provider that accepted an "+
			"undeclared image", c.Status)
	}
	if !res.Passed {
		t.Error("an unproven check failed the whole report; only a broken declaration should")
	}
}

// And a provider that refuses, as every provider in this repository does, is a
// clean skip.
func TestUndeclaredVisionThatRefusesIsASkip(t *testing.T) {
	caps := honest()
	caps.Vision = false
	res := CheckConformance(context.Background(), &blindRefuser{caps: caps}, ConformanceOptions{})

	if c := checkNamed(t, res, "vision"); c.Status != StatusSkip {
		t.Errorf("vision = %q (%s), want skip for a provider that refuses images",
			c.Status, c.Detail)
	}
}

type blindRefuser struct{ caps llm.Capabilities }

func (b *blindRefuser) Name() string                   { return "blind" }
func (b *blindRefuser) Capabilities() llm.Capabilities { return b.caps }
func (b *blindRefuser) Health(context.Context) error   { return nil }
func (b *blindRefuser) Close() error                   { return nil }
func (b *blindRefuser) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	if llm.HasImages(req.Messages) && !b.caps.Vision {
		return nil, &llm.UnsupportedError{Provider: "blind", Capability: "vision"}
	}
	if len(req.Tools) > 0 {
		return &llm.ChatResponse{
			FinishReason: "tool_calls",
			ToolCalls: []llm.ToolCall{{
				ID: "1", Name: "get_weather",
				Arguments: json.RawMessage(`{"city":"Berlin","units":"celsius"}`),
			}},
		}, nil
	}
	return &llm.ChatResponse{Content: "blue", OutputTokens: 2, FinishReason: "stop"}, nil
}
func (b *blindRefuser) ChatStructured(context.Context, llm.ChatRequest, json.RawMessage) (*llm.ChatResponse, error) {
	return &llm.ChatResponse{Content: `{"language":"Go","confident":true}`, OutputTokens: 9}, nil
}
func (b *blindRefuser) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return &llm.EmbedResponse{Vectors: [][]float32{{1, 2}, {3, 4}}, Dims: 2}, nil
}
func (b *blindRefuser) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "blind", Capability: "infill"}
}
