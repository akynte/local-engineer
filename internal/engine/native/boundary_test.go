package native_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/llm"
)

func TestContextBoundaryStopsBeforeOversizedRequest(t *testing.T) {
	p := newScripted(call("read", "read_file", map[string]any{"path": "large.go"}),
		call("done", "done", map[string]any{"summary": "done"}))
	e := newEngine(t, p)
	e.ContextTokens, e.MaxTokens = 2500, 32
	root := worktree(t, map[string]string{"large.go": strings.Repeat("large body ", 1000)})
	out, err := e.Step(context.Background(), engine.Request{Worktree: root})
	if err != nil {
		t.Fatal(err)
	}
	if !out.BudgetExhausted || out.ClaimsDone || len(p.requests) != 1 {
		t.Fatalf("expected boundary after one read: out=%+v requests=%d", out, len(p.requests))
	}
	// A seed that is already too large must never reach the provider.
	p = newScripted()
	e = newEngine(t, p)
	e.ContextTokens, e.MaxTokens = 1, 32
	out, err = e.Step(context.Background(), engine.Request{Worktree: root})
	if err != nil || !out.BudgetExhausted || len(p.requests) != 0 {
		t.Fatalf("oversized seed reached provider: %+v, %v", out, err)
	}
}

func TestEditTranscriptIsAnExactPrefixAcrossCalls(t *testing.T) {
	p := newScripted(call("read", "read_file", map[string]any{"path": "a.go"}),
		call("edit", "edit_file", map[string]any{"path": "a.go", "old": "old", "new": "new"}),
		call("done", "done", map[string]any{"summary": "done"}))
	e := newEngine(t, p)
	out, err := e.Step(context.Background(), engine.Request{
		Worktree: worktree(t, map[string]string{"a.go": "old"}),
		Access:   firewall.Access{WriteScope: []string{"a.go"}},
	})
	if err != nil || !out.Edited {
		t.Fatalf("edit failed: %+v %v", out, err)
	}
	for i := 1; i < len(p.requests); i++ {
		previous, next := p.requests[i-1], p.requests[i]
		if !reflect.DeepEqual(previous.Messages, next.Messages[:len(previous.Messages)]) ||
			!reflect.DeepEqual(previous.Tools, next.Tools) {
			t.Fatalf("request %d rewrote earlier context", i)
		}
	}
}

func TestUnauthorizedAndMalformedWritesHaveNoSideEffects(t *testing.T) {
	for _, tc := range []struct {
		name  string
		args  map[string]any
		scope []string
	}{
		{"no scope", map[string]any{"path": "a.go", "old": "old", "new": "new"}, nil},
		{"wrong scope", map[string]any{"path": "a.go", "old": "old", "new": "new"}, []string{"b.go"}},
		{"wrong type", map[string]any{"path": "a.go", "old": "old", "new": 12}, []string{"a.go"}},
		{"missing replacement", map[string]any{"path": "a.go", "old": "old"}, []string{"a.go"}},
		{"extra argument", map[string]any{"path": "a.go", "old": "old", "new": "new", "force": true}, []string{"a.go"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := worktree(t, map[string]string{"a.go": "old"})
			p := newScripted(call("edit", "edit_file", tc.args), &llm.ChatResponse{Content: "stopped"})
			out, err := newEngine(t, p).Step(context.Background(), engine.Request{Worktree: root, Access: firewall.Access{WriteScope: tc.scope}})
			if err != nil {
				t.Fatal(err)
			}
			body, err := os.ReadFile(filepath.Join(root, "a.go"))
			if err != nil || string(body) != "old" || out.Edited {
				t.Fatalf("denied edit changed file: %q, %v", body, err)
			}
			last := p.requests[len(p.requests)-1].Messages
			if !strings.Contains(last[len(last)-1].Content, "firewall:") {
				t.Fatal("no firewall feedback")
			}
		})
	}
}

func TestRequestTokenBudgetStopsBeforeProvider(t *testing.T) {
	p := newScripted()
	out, err := newEngine(t, p).Step(context.Background(), engine.Request{Budget: engine.Budget{MaxTokens: 1}})
	if err != nil || !out.BudgetExhausted || len(p.requests) != 0 {
		t.Fatalf("token limit ignored: %+v %v", out, err)
	}
}
