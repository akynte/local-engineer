package task_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/task"
)

type boundaryEngine struct {
	engine.Verify
	request engine.Request
}

func (e *boundaryEngine) Step(_ context.Context, req engine.Request) (*engine.Response, error) {
	e.request = req
	return &engine.Response{BudgetExhausted: true, TokensUsed: 12, Summary: "EDIT context budget exhausted"}, nil
}

func TestBoundaryBlocksTaskAndPreservesAuthority(t *testing.T) {
	ctx := context.Background()
	eng := &boundaryEngine{}
	r, st := newRunner(t, eng)
	repo := gitRepo(t, map[string]string{"README.md": "fixture"})
	id := task.NewID("boundary")
	// Defaulting attempts must not erase an explicit scope or token budget.
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "update README", Budget: task.Budget{Scope: []string{"README.md"}, MaxTokens: 100},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted || out.Task.State != task.StateBlocked || out.Attempts != 1 || len(out.Results) != 0 {
		t.Fatalf("boundary must block without verification or retries: %+v", out)
	}
	if eng.request.Journal == nil || !reflect.DeepEqual(eng.request.Access.WriteScope, []string{"README.md"}) || eng.request.Budget.MaxTokens != 100 {
		t.Fatalf("authority/budget not forwarded: %+v", eng.request)
	}
	_, state, _, _, err := ledger.New(st).LastCheckpoint(ctx, id)
	if err != nil || state != string(task.StateBlocked) {
		t.Fatalf("missing blocked checkpoint: %s %v", state, err)
	}
	ops, err := ledger.New(st).Operations(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, op := range ops {
		found = found || strings.Contains(string(op.Outcome), `"budget_exhausted":true`)
	}
	if !found {
		t.Fatal("boundary reason did not survive in the journal")
	}
}
