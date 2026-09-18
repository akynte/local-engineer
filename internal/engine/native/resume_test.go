package native_test

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/workflow"
)

func TestResumePreservesPrefixAndFence(t *testing.T) {
	ctx := context.Background()
	root := worktree(t, map[string]string{"a.go": "package a"})
	tr := &workflow.Transcript{}
	first := newScripted(call("read", "read_file", map[string]any{"path": "a.go"}))
	_, err := newEngine(t, first).Step(ctx, engine.Request{Worktree: root, Transcript: tr, SaveTranscript: func(_ context.Context, tr *workflow.Transcript) error {
		if tr.Tools == 1 {
			return errors.New("simulated interruption after durable result")
		}
		return nil
	}})
	if err == nil || len(tr.Messages) != 4 || tr.Pending {
		t.Fatalf("expected provider interruption after a completed read: %+v %v", tr, err)
	}
	prefix := append([]llm.Message(nil), tr.Messages...)
	second := newScripted(call("done", "done", map[string]any{"summary": "done"}))
	out, err := newEngine(t, second).Step(ctx, engine.Request{Worktree: root, Transcript: tr})
	if err != nil || !out.ClaimsDone {
		t.Fatalf("resume: %+v %v", out, err)
	}
	if !reflect.DeepEqual(prefix, second.requests[0].Messages) {
		t.Fatal("resume rebuilt the prefix")
	}
	if !strings.Contains(tr.Messages[len(tr.Messages)-1].Content, tr.FenceToken) {
		t.Fatal("resume changed the fence")
	}
}

func TestInterruptedBatchCannotReplay(t *testing.T) {
	ctx := context.Background()
	root := worktree(t, map[string]string{"a.go": "package a"})
	a := call("one", "read_file", map[string]any{"path": "a.go"})
	b := call("two", "read_file", map[string]any{"path": "a.go"})
	a.ToolCalls = append(a.ToolCalls, b.ToolCalls...)
	tr := &workflow.Transcript{}
	_, err := newEngine(t, newScripted(a)).Step(ctx, engine.Request{Worktree: root, Transcript: tr, SaveTranscript: func(_ context.Context, tr *workflow.Transcript) error {
		if tr.Tools == 1 {
			return errors.New("simulated process interruption")
		}
		return nil
	}})
	if err == nil || !tr.Pending {
		t.Fatalf("batch must remain pending: %+v %v", tr, err)
	}
	provider := newScripted()
	_, err = newEngine(t, provider).Step(ctx, engine.Request{Worktree: root, Transcript: tr})
	if err == nil || len(provider.requests) != 0 {
		t.Fatal("interrupted batch replayed")
	}
}

func TestCompletedTranscriptDoesNotCallModelOnResume(t *testing.T) {
	tr := &workflow.Transcript{}
	ctx := context.Background()
	out, err := newEngine(t, newScripted(call("done", "done", map[string]any{"summary": "completed"}))).Step(ctx, engine.Request{Transcript: tr})
	if err != nil || !out.ClaimsDone || !tr.Closed {
		t.Fatalf("completion was not saved: %+v %v", tr, err)
	}
	provider := newScripted()
	out, err = newEngine(t, provider).Step(ctx, engine.Request{Transcript: tr})
	if err != nil || !out.ClaimsDone || out.Summary != "completed" || len(provider.requests) != 0 {
		t.Fatalf("completion replayed: %+v %v", out, err)
	}
}
