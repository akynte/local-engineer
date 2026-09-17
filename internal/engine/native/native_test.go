package native_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/akynte/local-engineer/internal/workspace"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/engine/native"
	"github.com/akynte/local-engineer/internal/graph"
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
//
// Every call here asks something different, so the loop is making progress by
// the progress guard's reckoning and only the step limit can stop it. That is
// the property this test exists for: a model that keeps learning still costs a
// bounded number of steps.
func TestTheLoopIsBounded(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	var responses []*llm.ChatResponse
	for i := 0; i < 50; i++ {
		responses = append(responses, call(fmt.Sprint(i), "list_files",
			map[string]any{"path": fmt.Sprintf("dir%d", i)}))
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

// TestTruncatedThinkingIsNotAStop pins the difference between a model that
// decided it was finished and one whose output budget ran out mid-thought.
// Both arrive as a response with no tool calls, so before the distinction
// existed a reasoning model that spent its whole budget thinking was recorded
// as "the model stopped after 1 step(s) without calling a tool" — a harness
// limit reported as a model verdict.
func TestTruncatedThinkingIsNotAStop(t *testing.T) {
	wt := worktree(t, map[string]string{"calc.go": "package calc\n"})
	p := newScripted(&llm.ChatResponse{
		FinishReason: "length",
		Content:      "",
		Reasoning:    strings.Repeat("let me consider the call sites. ", 200),
	})
	e := newEngine(t, p)

	resp, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "fix Add", Worktree: wt, Attempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated {
		t.Fatal("a response cut off at the output budget was not reported as truncated")
	}
	if resp.ClaimsDone {
		t.Error("a truncated response must not claim completion")
	}
	if !strings.Contains(resp.Summary, "output budget") {
		t.Errorf("summary does not name the limit that was hit: %q", resp.Summary)
	}
	if !strings.Contains(resp.Summary, "reasoning") {
		t.Errorf("summary does not say the budget went to reasoning, which is the "+
			"actionable part: %q", resp.Summary)
	}
}

// TestFinishedWithoutToolsIsStillAStop is the other side of the same boundary:
// a model that answers in prose and stops is a genuine stop, not truncation.
func TestFinishedWithoutToolsIsStillAStop(t *testing.T) {
	wt := worktree(t, map[string]string{"calc.go": "package calc\n"})
	p := newScripted(&llm.ChatResponse{FinishReason: "stop", Content: "Add is already correct."})
	e := newEngine(t, p)

	resp, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "fix Add", Worktree: wt, Attempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Truncated {
		t.Error("a completed response was reported as truncated")
	}
	if resp.Summary != "Add is already correct." {
		t.Errorf("summary = %q, want the model's own answer", resp.Summary)
	}
}

// TestTruncatedWithPartialContentIsNotTruncation guards the narrow condition:
// a response that hit the length limit but still produced usable text is the
// model's answer, cut short. Only an empty one is budget exhaustion.
func TestTruncatedWithPartialContentIsNotTruncation(t *testing.T) {
	wt := worktree(t, map[string]string{"calc.go": "package calc\n"})
	p := newScripted(&llm.ChatResponse{FinishReason: "length", Content: "I changed the sign in"})
	e := newEngine(t, p)

	resp, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "fix Add", Worktree: wt, Attempt: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Truncated {
		t.Error("a response carrying partial content was treated as budget exhaustion")
	}
}

// TestUnwiredToolsAreNotAdvertised is the fix for an ablation that leaked. The
// baseline arm is built by leaving retrieval, the graph and verification
// unwired — but the engine advertised those tools anyway, so the model spent
// calls on them and had each one rejected. In one measured run 15 of 31 tool
// calls went to tools that could not run, against a wall-clock budget. The
// baseline was charged for the components it was supposed to be measured
// without.
func TestUnwiredToolsAreNotAdvertised(t *testing.T) {
	wt := worktree(t, map[string]string{"calc.go": "package calc\n"})
	p := newScripted(&llm.ChatResponse{FinishReason: "stop", Content: "done"})
	e := newEngine(t, p) // no Retriever, no Graph, no Recipes

	if _, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "fix Add", Worktree: wt, Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	offered := map[string]bool{}
	for _, tool := range p.lastRequest(t).Tools {
		offered[tool.Name] = true
	}
	for _, name := range []string{"search_code", "find_symbol", "impact_of", "run_verification"} {
		if offered[name] {
			t.Errorf("%s was advertised to a model that cannot run it", name)
		}
	}
	// The tools that need nothing but the worktree must still be there, or the
	// baseline is not a fair version of the model.
	for _, name := range []string{"read_file", "edit_file", "list_files", "done"} {
		if !offered[name] {
			t.Errorf("%s was withheld from the baseline", name)
		}
	}
}

// TestSetRecipeRunnerAddsTheTool pins the other direction: the recipe runner is
// injected after the engine is built, so the surface has to be recomputed or a
// supervised run never learns it can verify.
func TestSetRecipeRunnerAddsTheTool(t *testing.T) {
	wt := worktree(t, map[string]string{"calc.go": "package calc\n"})
	p := newScripted(&llm.ChatResponse{FinishReason: "stop", Content: "done"})
	e := newEngine(t, p)

	if names := toolNames(t, e, p, wt); names["run_verification"] {
		t.Fatal("run_verification was advertised before a recipe runner was set")
	}
	e.SetRecipeRunner(&recipe.Runner{})
	if names := toolNames(t, e, p, wt); !names["run_verification"] {
		t.Error("run_verification was not advertised after a recipe runner was set")
	}
}

func toolNames(t *testing.T, e *native.Engine, p *scripted, wt string) map[string]bool {
	t.Helper()
	p.mu.Lock()
	p.responses = append(p.responses, &llm.ChatResponse{FinishReason: "stop", Content: "done"})
	p.mu.Unlock()
	if _, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "o", Worktree: wt, Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, tool := range p.lastRequest(t).Tools {
		out[tool.Name] = true
	}
	return out
}

// §11 adopts "repository memory from commit history" as a cheap extra signal,
// naming git_touch as the tool. The gitlog analyzer wrote commit-to-file edges
// and nothing could read them.
func TestGitTouchReadsIndexedHistory(t *testing.T) {
	g := &fakeGraph{
		byName: map[string][]graph.Node{
			"payment.go": {{ID: 1, Kind: graph.KindFile, Name: "payment.go", Path: "internal/payment.go"}},
		},
		incoming: map[int64][]graph.Edge{
			1: {{Src: 10, Dst: 1, Kind: graph.EdgeTouches, Evidence: graph.Observed}},
		},
		nodes: map[int64]graph.Node{
			10: {
				ID: 10, Kind: graph.KindCommit, Name: "a1b2c3d",
				Signature: "handle the refund case",
				Attrs:     `{"author":"someone","when":"2026-03-04T10:00:00Z","files":3}`,
			},
		},
	}
	wt := worktree(t, map[string]string{"internal/payment.go": "package payment\n"})
	p := newScripted(
		call("1", "git_touch", map[string]any{"path": "internal/payment.go"}),
		&llm.ChatResponse{FinishReason: "stop", Content: "read the history"},
	)
	e, err := native.New(native.Options{Provider: p, Graph: g, MaxSteps: 5, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "why is this here", Worktree: wt, Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}

	// The tool result reaches the model as a tool turn.
	var toolContent string
	for _, m := range p.lastRequest(t).Messages {
		if m.Role == "tool" {
			toolContent = m.Content
		}
	}
	for _, want := range []string{"a1b2c3d", "handle the refund case", "someone", "2026-03-04"} {
		if !strings.Contains(toolContent, want) {
			t.Errorf("history output does not carry %q:\n%s", want, toolContent)
		}
	}
	// The clock time is noise at this level.
	if strings.Contains(toolContent, "10:00:00") {
		t.Errorf("the full timestamp was included:\n%s", toolContent)
	}
}

// Without a graph there is no indexed history, and the tool must say so rather
// than reporting an empty result that reads as "this file has no history".
func TestGitTouchWithoutAGraphSaysSo(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	p := newScripted(
		call("1", "git_touch", map[string]any{"path": "a.go"}),
		&llm.ChatResponse{FinishReason: "stop", Content: "done"},
	)
	e := newEngine(t, p) // no graph
	if _, err := e.Step(context.Background(), engine.Request{
		TaskID: "t1", Objective: "o", Worktree: wt, Attempt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	// And it is not even advertised, since the engine cannot run it.
	for _, tool := range p.lastRequest(t).Tools {
		if tool.Name == "git_touch" {
			t.Error("git_touch was advertised to an engine with no graph")
		}
	}
}

// fakeGraph answers the three calls git_touch makes and refuses the rest, so a
// test that accidentally depends on something else fails loudly.
type fakeGraph struct {
	byName   map[string][]graph.Node
	incoming map[int64][]graph.Edge
	nodes    map[int64]graph.Node
}

func (f *fakeGraph) WorkspaceID() workspace.ID { return workspace.ID("fakefakefakefakefakefakefa") }

func (f *fakeGraph) NodesByName(_ context.Context, name string, _ []graph.NodeKind, _ int) ([]graph.Node, error) {
	return f.byName[name], nil
}

func (f *fakeGraph) Neighbors(_ context.Context, id int64, dir graph.Direction, _ []graph.EdgeKind) ([]graph.Edge, error) {
	if dir != graph.Reverse {
		return nil, nil
	}
	return f.incoming[id], nil
}

func (f *fakeGraph) Node(_ context.Context, id int64) (graph.Node, error) {
	n, ok := f.nodes[id]
	if !ok {
		return graph.Node{}, fmt.Errorf("no node %d", id)
	}
	return n, nil
}

func (f *fakeGraph) UpsertNode(context.Context, graph.Node) (int64, error) {
	return 0, errors.New("not used by these tests")
}
func (f *fakeGraph) UpsertNodes(context.Context, []graph.Node) ([]int64, error) {
	return nil, errors.New("not used by these tests")
}
func (f *fakeGraph) UpsertEdges(context.Context, []graph.Edge) error {
	return errors.New("not used by these tests")
}
func (f *fakeGraph) NodeByFQN(context.Context, string, graph.NodeKind, string) (graph.Node, error) {
	return graph.Node{}, errors.New("not used by these tests")
}
func (f *fakeGraph) Traverse(context.Context, graph.Query) ([]graph.Reached, error) {
	return nil, errors.New("not used by these tests")
}
func (f *fakeGraph) ImpactOf(context.Context, []int64, graph.ChangeKind) (graph.Impact, error) {
	return graph.Impact{}, errors.New("not used by these tests")
}

func (f *fakeGraph) Stats(context.Context) (graph.Stats, error) {
	return graph.Stats{}, errors.New("not used by these tests")
}

// A model stuck calling the same tool must be stopped while there is still
// budget left to explain it, not at the step limit long afterwards. This is the
// end-to-end version: a real engine, a real tool, a provider that never varies.
func TestARepeatingModelIsStoppedBeforeTheStepLimit(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	var responses []*llm.ChatResponse
	for i := 0; i < 50; i++ {
		responses = append(responses, call(fmt.Sprint(i), "read_file", map[string]any{"path": "a.go"}))
	}
	p := newScripted(responses...)
	e, err := native.New(native.Options{Provider: p, MaxSteps: 40, Logf: t.Logf})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1})
	if err != nil {
		t.Fatal(err)
	}
	if resp.ClaimsDone {
		t.Error("a looping model must not be reported as done")
	}
	p.mu.Lock()
	calls := len(p.requests)
	p.mu.Unlock()
	if calls >= 40 {
		t.Errorf("the loop ran to the step limit (%d calls); the guard did not fire", calls)
	}
	if calls > 5 {
		t.Errorf("the guard took %d steps to notice an identical repeated call", calls)
	}
	// The operator has to be able to read what happened from the summary.
	if !strings.Contains(resp.Summary, "read_file") {
		t.Errorf("the summary must name what it kept doing, got %q", resp.Summary)
	}
	t.Logf("stopped after %d model calls: %s", calls, resp.Summary)
}

// Before it gives up, the supervisor tells the model it is repeating itself —
// and that warning is the supervisor speaking, so it must not be inside the
// untrusted fence.
func TestTheRepeatWarningReachesTheModelOutsideTheFence(t *testing.T) {
	wt := worktree(t, map[string]string{"a.go": "package a\n"})
	p := newScripted(
		call("1", "read_file", map[string]any{"path": "a.go"}),
		call("2", "read_file", map[string]any{"path": "a.go"}),
		call("3", "done", map[string]any{"summary": "x"}),
	)
	e := newEngine(t, p)
	if _, err := e.Step(context.Background(), engine.Request{Worktree: wt, Attempt: 1}); err != nil {
		t.Fatal(err)
	}

	var warned string
	for _, m := range p.lastRequest(t).Messages {
		if m.Role == "tool" && strings.Contains(m.Content, "[supervisor]") {
			warned = m.Content
		}
	}
	if warned == "" {
		t.Fatal("the model was never told it was repeating itself")
	}
	head, _, ok := strings.Cut(warned, "<<<UNTRUSTED")
	if !ok {
		t.Fatal("the tool result was not fenced")
	}
	if !strings.Contains(head, "[supervisor]") {
		t.Error("the supervisor's warning was inside the untrusted fence, which tells " +
			"the model to treat its own supervisor as repository data")
	}
}
