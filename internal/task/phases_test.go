package task_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
	"github.com/akynte/local-engineer/internal/workflow"
)

type phaseModel struct {
	llm.Provider
	calls  int
	accept bool
	// frozen is the prefix the first call saw. Every later call in the task
	// must present the same bytes, which is what makes the prompt an exact
	// prefix extension and the difference between a warm cache and minutes of
	// re-prefill on the target hardware (§7.1).
	frozen []string
}

func (p *phaseModel) Capabilities() llm.Capabilities { return llm.Capabilities{StructuredOutput: true} }
func (p *phaseModel) ChatStructured(_ context.Context, req llm.ChatRequest, _ json.RawMessage) (*llm.ChatResponse, error) {
	p.calls++
	if len(req.Tools) != 0 {
		panic("a structured phase call carried tools")
	}
	if req.CachePrefixHint < 2 || req.CachePrefixHint > len(req.Messages) {
		panic("structured phase call did not declare its frozen prefix")
	}
	for _, m := range req.Messages {
		if m.Role == "assistant" {
			panic("phase call inherited a conversation")
		}
	}
	prefix := make([]string, req.CachePrefixHint)
	for i := range prefix {
		prefix[i] = req.Messages[i].Content
	}
	if p.frozen == nil {
		p.frozen = prefix
	} else if len(prefix) != len(p.frozen) {
		panic("the frozen prefix changed length between phase calls")
	} else {
		for i := range prefix {
			// The task card legitimately gains the plan once, at the planning
			// boundary. Nothing else in the prefix may move.
			if prefix[i] != p.frozen[i] && i == 0 {
				panic("the operating policy changed between phase calls")
			}
		}
		p.frozen = prefix
	}

	// The phase instruction is in the tail, which is the last message.
	instruction := req.Messages[len(req.Messages)-1].Content
	body := `{"files":["a.go"],"symbols":["Add"],"hypothesis":"addition needs correction"}`
	if strings.Contains(instruction, "executable plan") {
		body = `{"root_cause":"addition needs correction","files":["a.go"],"symbols":["Add"],"tests":["go test ./..."],"contracts":[],"write_allowlist":["a.go"],"risks":[]}`
	}
	if strings.Contains(instruction, "Review this change") {
		body = `{"accept":false,"findings":["contract unmet"]}`
		if p.accept {
			body = `{"accept":true,"findings":[]}`
		}
	}
	return &llm.ChatResponse{Content: body, PromptTokens: 10, OutputTokens: 10}, nil
}

type phaseEditor struct {
	engine.Verify
	calls int
}

func (*phaseEditor) Edits() bool { return true }
func (e *phaseEditor) Step(_ context.Context, req engine.Request) (*engine.Response, error) {
	e.calls++
	if req.Phase != workflow.Edit || req.Plan == nil {
		panic("missing validated plan")
	}
	if err := req.Access.Check(req.Worktree, "a.go", true); err != nil {
		return nil, err
	}
	err := os.WriteFile(filepath.Join(req.Worktree, "a.go"), []byte("package a\n\nfunc Add(x, y int) int { return x + y }\n"), 0644)
	return &engine.Response{Edited: true, ClaimsDone: true}, err
}

func TestWorkflowRequiresIndependentAcceptance(t *testing.T) {
	for _, accept := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "accepted"}[accept], func(t *testing.T) {
			requireGo(t)
			repo := gitRepo(t, map[string]string{"go.mod": goodModule, "a.go": "package a\n\nfunc Add(x, y int) int { return x - y }\n"})
			editor := &phaseEditor{}
			r, st := newRunner(t, editor)
			model := &phaseModel{accept: accept}
			r.WorkflowModel = model
			ctx := context.Background()
			id := task.NewID("phases")
			if err := task.NewStore(st).Create(ctx, task.Task{ID: id, Title: "correct addition", Verification: recipe.Standard, Budget: task.Budget{MaxAttempts: 1}}); err != nil {
				t.Fatal(err)
			}
			out, err := r.Run(ctx, id, repo)
			if err != nil {
				t.Fatal(err)
			}
			if out.Accepted != accept {
				t.Fatalf("accept=%v outcome=%+v", accept, out)
			}
			if model.calls != 5 || editor.calls != 1 {
				t.Fatalf("structured=%d edit=%d", model.calls, editor.calls)
			}
			s, err := task.NewStore(st).LoadWorkflow(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if s.Verdict == nil || s.Verdict.Accept != accept || s.Tokens != 100 {
				t.Fatalf("state=%+v", s)
			}
		})
	}
}
