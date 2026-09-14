package task_test

import (
	"context"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

// A plan is produced by a model and is not trusted by one. Every rule here
// exists because the failure it prevents would otherwise be discovered
// mid-execution, with a half-applied change in a worktree.

func validStep(title string) task.PlanStep {
	return task.PlanStep{Title: title, Scope: []string{"internal/service"}, Verification: recipe.Standard}
}

func TestAValidPlanIsAccepted(t *testing.T) {
	p := task.Plan{Steps: []task.PlanStep{
		validStep("add the field"),
		{Title: "use it", Scope: []string{"internal/handler"},
			Verification: recipe.Standard, DependsOn: []int{1}},
	}}
	if err := p.Validate(8); err != nil {
		t.Fatalf("a well-formed plan was rejected: %v", err)
	}
}

func TestAnEmptyPlanIsRejected(t *testing.T) {
	empty := task.Plan{}
	if err := empty.Validate(8); err == nil {
		t.Fatal("a plan with no steps is not a plan")
	}
}

// Scope is how the supervisor detects a step doing more than it should.
// Without one, the out-of-scope check cannot fire at all.
func TestAStepWithoutScopeIsRejected(t *testing.T) {
	p := task.Plan{Steps: []task.PlanStep{
		{Title: "do everything", Verification: recipe.Standard},
	}}
	err := p.Validate(8)
	if err == nil {
		t.Fatal("a step with no declared scope must be rejected")
	}
	if !strings.Contains(err.Error(), "out-of-scope check") {
		t.Errorf("the rejection should explain why scope matters: %v", err)
	}
}

func TestScopeEscapingTheRepositoryIsRejected(t *testing.T) {
	for _, bad := range []string{"/etc", "../other", "internal/../../elsewhere"} {
		p := task.Plan{Steps: []task.PlanStep{
			{Title: "x", Scope: []string{bad}, Verification: recipe.Standard},
		}}
		if err := p.Validate(8); err == nil {
			t.Errorf("scope %q escapes the repository and must be rejected", bad)
		}
	}
}

func TestAnUnknownVerificationLevelIsRejected(t *testing.T) {
	p := task.Plan{Steps: []task.PlanStep{
		{Title: "x", Scope: []string{"internal"}, Verification: "paranoid"},
	}}
	if err := p.Validate(8); err == nil {
		t.Fatal("an unknown verification level must be rejected")
	}
}

// A forward or self dependency cannot be satisfied by running the plan in
// order, and a cycle would deadlock the run.
func TestForwardAndSelfDependenciesAreRejected(t *testing.T) {
	cases := map[string][]int{
		"forward": {2},
		"self":    {1},
		"absent":  {9},
	}
	for name, deps := range cases {
		t.Run(name, func(t *testing.T) {
			p := task.Plan{Steps: []task.PlanStep{
				{Title: "first", Scope: []string{"a"}, Verification: recipe.Standard, DependsOn: deps},
				validStep("second"),
			}}
			if err := p.Validate(8); err == nil {
				t.Fatalf("a %s dependency must be rejected", name)
			}
		})
	}
}

// A decomposition into twenty steps is a to-do list, and each step costs a
// full verification run.
func TestAnOversizedPlanIsRejected(t *testing.T) {
	var steps []task.PlanStep
	for i := 0; i < 12; i++ {
		steps = append(steps, validStep("step"))
	}
	oversized := task.Plan{Steps: steps}
	err := oversized.Validate(8)
	if err == nil {
		t.Fatal("a plan beyond the step limit must be rejected")
	}
	if !strings.Contains(err.Error(), "verification run") {
		t.Errorf("the rejection should explain the cost: %v", err)
	}
}

func TestAStepWithoutATitleIsRejected(t *testing.T) {
	p := task.Plan{Steps: []task.PlanStep{
		{Title: "   ", Scope: []string{"a"}, Verification: recipe.Standard},
	}}
	if err := p.Validate(8); err == nil {
		t.Fatal("a step with no title is not actionable")
	}
}

// Materialise turns a validated plan into child tasks with their dependencies,
// and a task with an unfinished dependency is blocked.
func TestMaterialiseCreatesChildTasksWithDependencies(t *testing.T) {
	ctx := context.Background()
	_, st := newRunner(t, nil)
	store := task.NewStore(st)

	parent := task.Task{ID: task.NewID("parent"), Title: "the requirement"}
	if err := store.Create(ctx, parent); err != nil {
		t.Fatal(err)
	}

	plan := &task.Plan{
		Objective: "split the service",
		Steps: []task.PlanStep{
			{Title: "add the repository method", Scope: []string{"internal/repository"},
				Verification: recipe.Standard},
			{Title: "call it from the service", Scope: []string{"internal/service"},
				Verification: recipe.Standard, DependsOn: []int{1}},
		},
	}
	children, err := store.Materialise(ctx, plan, parent.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 2 {
		t.Fatalf("expected 2 child tasks, got %d", len(children))
	}
	for i, c := range children {
		if c.ParentID != parent.ID {
			t.Errorf("child %d has parent %q", i, c.ParentID)
		}
		if len(c.Budget.Scope) == 0 {
			t.Errorf("child %d lost its scope", i)
		}
	}

	// The second step depends on the first, which has not been accepted.
	blockers, err := store.Blockers(ctx, children[1].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(blockers) != 1 || blockers[0].ID != children[0].ID {
		t.Fatalf("expected the first step to block the second, got %+v", blockers)
	}
	// The first step blocks on nothing.
	if b, _ := store.Blockers(ctx, children[0].ID); len(b) != 0 {
		t.Errorf("the first step should have no blockers, got %+v", b)
	}

	// Once the first is accepted, the second is unblocked.
	if err := store.SetState(ctx, children[0].ID, task.StateAccepted); err != nil {
		t.Fatal(err)
	}
	blockers, _ = store.Blockers(ctx, children[1].ID)
	if len(blockers) != 0 {
		t.Errorf("an accepted dependency should no longer block, got %+v", blockers)
	}
}

// An invalid plan must never reach the database as half-created tasks.
func TestMaterialiseRefusesAnInvalidPlan(t *testing.T) {
	ctx := context.Background()
	_, st := newRunner(t, nil)
	store := task.NewStore(st)

	_, err := store.Materialise(ctx, &task.Plan{Steps: []task.PlanStep{
		{Title: "unbounded", Verification: recipe.Standard},
	}}, "")
	if err == nil {
		t.Fatal("an invalid plan must be refused before any task is created")
	}
	tasks, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 0 {
		t.Errorf("a rejected plan left %d tasks behind", len(tasks))
	}
}
