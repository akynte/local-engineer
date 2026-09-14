package native_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/engine/native"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
)

// scripted is a provider that replays a fixed sequence of responses and
// records what it was sent. It stands in for a model so the *loop* can be
// tested deterministically; the wire formats themselves are tested in
// internal/llm against httptest servers.
type scripted struct {
	mu        sync.Mutex
	responses []*llm.ChatResponse
	requests  []llm.ChatRequest
	caps      llm.Capabilities
}

func newScripted(responses ...*llm.ChatResponse) *scripted {
	return &scripted{
		responses: responses,
		caps:      llm.Capabilities{Kind: llm.KindLlamaCPP, ToolCalling: true, Local: true},
	}
}

func (s *scripted) Name() string                   { return "scripted" }
func (s *scripted) Capabilities() llm.Capabilities { return s.caps }
func (s *scripted) Health(context.Context) error   { return nil }
func (s *scripted) Close() error                   { return nil }

func (s *scripted) Chat(_ context.Context, req llm.ChatRequest) (*llm.ChatResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.requests = append(s.requests, req)
	if len(s.responses) == 0 {
		return &llm.ChatResponse{Content: "nothing further"}, nil
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return r, nil
}

func (s *scripted) ChatStructured(context.Context, llm.ChatRequest, json.RawMessage) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "scripted", Capability: "structured output"}
}
func (s *scripted) Embed(context.Context, llm.EmbedRequest) (*llm.EmbedResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "scripted", Capability: "embeddings"}
}
func (s *scripted) Infill(context.Context, llm.InfillRequest) (*llm.ChatResponse, error) {
	return nil, &llm.UnsupportedError{Provider: "scripted", Capability: "infill"}
}

func (s *scripted) lastRequest(t *testing.T) llm.ChatRequest {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.requests) == 0 {
		t.Fatal("the provider was never called")
	}
	return s.requests[len(s.requests)-1]
}

// call builds a tool-call response.
func call(id, name string, args map[string]any) *llm.ChatResponse {
	body, _ := json.Marshal(args)
	return &llm.ChatResponse{
		FinishReason: "tool_calls",
		ToolCalls:    []llm.ToolCall{{ID: id, Name: name, Arguments: body}},
	}
}

func worktree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func newEngine(t *testing.T, p llm.Provider) *native.Engine {
	t.Helper()
	e, err := native.New(native.Options{Provider: p, MaxSteps: 10, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func TestEngineReadsEditsAndDeclaresDone(t *testing.T) {
	wt := worktree(t, map[string]string{
		"calc.go": "package calc\n\nfunc Add(x, y int) int { return x - y }\n",
	})
	p := newScripted(
		call("1", "read_file", map[string]any{"path": "calc.go"}),
		call("2", "edit_file", map[string]any{
			"path": "calc.go", "old": "return x - y", "new": "return x + y"}),
		call("3", "done", map[string]any{"summary": "fixed the sign"}),
	)
	e := newEngine(t, p)

	resp, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "fix Add", Worktree: wt, Attempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.ClaimsDone {
		t.Error("the engine should report the model's completion claim")
	}
	if resp.Summary != "fixed the sign" {
		t.Errorf("summary = %q", resp.Summary)
	}
	if !resp.Edited {
		t.Error("an edit must be reported")
	}
	body, _ := os.ReadFile(filepath.Join(wt, "calc.go"))
	if !strings.Contains(string(body), "x + y") {
		t.Fatalf("the edit did not land:\n%s", body)
	}
}

// The tool surface and the objective must reach the model, and the tool
// schemas must be declared — a model given no schema produces malformed calls.
func TestToolsAndObjectiveReachTheModel(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	p := newScripted(&llm.ChatResponse{Content: "done thinking"})
	e := newEngine(t, p)

	if _, err := e.Step(context.Background(), engine.Request{
		Objective: "make the widget work", Worktree: wt, Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	req := p.lastRequest(t)
	if len(req.Tools) == 0 {
		t.Fatal("no tools were declared")
	}
	for _, tool := range req.Tools {
		if len(tool.Schema) == 0 {
			t.Errorf("tool %s has no schema; a model without one produces malformed calls", tool.Name)
		}
		if tool.Description == "" {
			t.Errorf("tool %s has no description", tool.Name)
		}
	}
	if req.Messages[0].Role != "system" {
		t.Error("the stable system prompt must come first so the prompt cache survives the loop")
	}
	if !strings.Contains(req.Messages[1].Content, "make the widget work") {
		t.Errorf("the objective did not reach the model: %q", req.Messages[1].Content)
	}
}

// A failed tool call is a result the model can act on, not an error that
// throws away the turn.
func TestAFailedToolCallIsReportedBackAndTheLoopContinues(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	p := newScripted(
		call("1", "read_file", map[string]any{"path": "does-not-exist.go"}),
		call("2", "done", map[string]any{"summary": "recovered"}),
	)
	e := newEngine(t, p)

	resp, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1})
	if err != nil {
		t.Fatalf("a bad tool argument must not abort the step: %v", err)
	}
	if !resp.ClaimsDone {
		t.Error("the loop should have continued to the next call")
	}

	req := p.lastRequest(t)
	var toolReply string
	for _, m := range req.Messages {
		if m.Role == "tool" {
			toolReply = m.Content
		}
	}
	if !strings.Contains(toolReply, "does not exist") {
		t.Errorf("the model must be told what went wrong, got %q", toolReply)
	}
	if !strings.Contains(toolReply, "list_files") {
		t.Errorf("a correction should point at what to do instead, got %q", toolReply)
	}
}

// Every file operation is confined to the worktree by this package as well as
// by the sandbox. One layer of path checking is one bug away from none.
func TestPathEscapesAreRefused(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	secret := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(secret, []byte("another project"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, attempt := range []struct {
		name string
		args map[string]any
	}{
		{"parent traversal", map[string]any{"path": "../../etc/passwd"}},
		{"absolute path", map[string]any{"path": secret}},
		{"embedded traversal", map[string]any{"path": "sub/../../outside.go"}},
	} {
		t.Run(attempt.name, func(t *testing.T) {
			p := newScripted(
				call("1", "read_file", attempt.args),
				call("2", "done", map[string]any{"summary": "x"}),
			)
			e := newEngine(t, p)
			if _, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1}); err != nil {
				t.Fatal(err)
			}
			var reply string
			for _, m := range p.lastRequest(t).Messages {
				if m.Role == "tool" {
					reply = m.Content
					break
				}
			}
			if !strings.Contains(reply, "outside the worktree") {
				t.Errorf("the escape was not refused: %q", reply)
			}
			if strings.Contains(reply, "another project") {
				t.Fatal("the tool leaked content from outside the worktree")
			}
		})
	}
}

// A symlink planted inside the worktree must not become an escape hatch.
func TestSymlinkEscapeIsRefused(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("elsewhere"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(wt, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	p := newScripted(
		call("1", "read_file", map[string]any{"path": "link/secret.txt"}),
		call("2", "done", map[string]any{"summary": "x"}),
	)
	e := newEngine(t, p)
	if _, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	var reply string
	for _, m := range p.lastRequest(t).Messages {
		if m.Role == "tool" {
			reply = m.Content
			break
		}
	}
	if strings.Contains(reply, "elsewhere") {
		t.Fatal("a symlink inside the worktree leaked content from outside it")
	}
	if !strings.Contains(reply, "outside the worktree") {
		t.Errorf("reply = %q", reply)
	}
}

// An ambiguous edit is how a change silently lands on the wrong line.
func TestAmbiguousEditIsRefused(t *testing.T) {
	wt := worktree(t, map[string]string{
		"a.go": "package a\n\nfunc A() { x := 1; _ = x }\nfunc B() { x := 1; _ = x }\n",
	})
	p := newScripted(
		call("1", "edit_file", map[string]any{"path": "a.go", "old": "x := 1", "new": "x := 2"}),
		call("2", "done", map[string]any{"summary": "x"}),
	)
	e := newEngine(t, p)
	if _, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1}); err != nil {
		t.Fatal(err)
	}

	var reply string
	for _, m := range p.lastRequest(t).Messages {
		if m.Role == "tool" {
			reply = m.Content
			break
		}
	}
	if !strings.Contains(reply, "appears 2 times") {
		t.Errorf("an ambiguous edit must be refused with the count, got %q", reply)
	}
	body, _ := os.ReadFile(filepath.Join(wt, "a.go"))
	if strings.Contains(string(body), "x := 2") {
		t.Fatal("an ambiguous edit was applied anyway")
	}
}

// Overwriting an existing file wholesale discards code the model may never
// have read, so write_file steers to edit_file.
func TestWriteFileRefusesToClobberAnExistingFile(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n\nfunc Important() {}\n"})
	p := newScripted(
		call("1", "write_file", map[string]any{"path": "a.go", "content": "package a\n"}),
		call("2", "done", map[string]any{"summary": "x"}),
	)
	e := newEngine(t, p)
	if _, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(filepath.Join(wt, "a.go"))
	if !strings.Contains(string(body), "Important") {
		t.Fatal("write_file clobbered an existing file")
	}
}

// The loop is the supervisor's, and it is bounded.
func TestTheLoopIsBounded(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	var responses []*llm.ChatResponse
	for i := 0; i < 50; i++ {
		responses = append(responses, call(fmt.Sprint(i), "list_files", map[string]any{}))
	}
	p := newScripted(responses...)
	e, err := native.New(native.Options{Provider: p, MaxSteps: 4, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ClaimsDone {
		t.Error("a model that never called done must not be reported as done")
	}
	if !strings.Contains(resp.Summary, "step limit") {
		t.Errorf("summary should name the bound, got %q", resp.Summary)
	}
	p.mu.Lock()
	calls := len(p.requests)
	p.mu.Unlock()
	if calls != 4 {
		t.Errorf("the loop ran %d times, the bound is 4", calls)
	}
}

// Verification findings from the previous attempt must reach the next one:
// this is the compiler-and-test correction loop the design calls the core.
func TestPreviousFindingsAreGivenToTheNextAttempt(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	p := newScripted(&llm.ChatResponse{Content: "ok"})
	e := newEngine(t, p)

	if _, err := e.Step(context.Background(), engine.Request{
		Objective: "fix it", Worktree: wt, Attempt: 2,
		Feedback: []recipe.Result{{
			Recipe: "go test", Kind: recipe.KindTest, Status: recipe.Fail,
			Summary: recipe.Summary{
				Headline: "1 test(s) failed in 1 package(s)",
				Findings: []recipe.Finding{
					{File: "calc_test.go", Line: 11, Test: "TestAdd", Message: "got -1, want 5"},
				},
			},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	brief := p.lastRequest(t).Messages[1].Content
	if !strings.Contains(brief, "attempt 2") {
		t.Errorf("the model should be told this is a retry: %q", brief)
	}
	// The finding itself must reach the model, with its location: this is the
	// correction signal the design calls the core of the system.
	for _, want := range []string{"calc_test.go:11", "TestAdd", "got -1, want 5"} {
		if !strings.Contains(brief, want) {
			t.Errorf("the previous finding must reach the next attempt; missing %q in:\n%s", want, brief)
		}
	}
}

// A provider that does not declare tool calling is refused rather than driven
// by parsing prose, which would turn every malformed response into a no-op.
func TestProviderWithoutToolCallingIsRefused(t *testing.T) {
	p := newScripted()
	p.caps.ToolCalling = false
	if _, err := native.New(native.Options{Provider: p}); err == nil {
		t.Fatal("expected a refusal")
	}
}
