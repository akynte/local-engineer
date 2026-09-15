package task_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

// Rule 6 of the completion contract. This is not a hypothetical: a task whose
// engine read fifteen files and edited nothing was accepted with build, vet,
// test, race, gofmt and lint all green, because every one of those checks was
// answering a question about the baseline.
func TestATaskThatChangedNothingIsNotAccepted(t *testing.T) {
	const c = "unchanged"
	green := []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
	}

	ok, reasons := task.Accept(recipe.Standard, green, c, nil,
		task.Effect{Made: false, Expected: true})

	if ok {
		t.Fatalf("a task that changed nothing was accepted: %v", reasons)
	}
	// The reason has to name the real problem, or the next reader concludes
	// the checks failed and goes looking in the wrong place.
	var named bool
	for _, r := range reasons {
		if strings.Contains(r, "changed nothing") {
			named = true
		}
	}
	if !named {
		t.Errorf("the refusal must say the task changed nothing, got %v", reasons)
	}
}

// The same green evidence is acceptable once the task actually did something.
func TestTheSameEvidenceIsAcceptedWhenTheTaskChangedSomething(t *testing.T) {
	const c = "changed"
	green := []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
	}

	ok, reasons := task.Accept(recipe.Standard, green, c, nil,
		task.Effect{Made: true, Expected: true})

	if !ok {
		t.Fatalf("expected acceptance, got: %v", reasons)
	}
}

// `le task verify` puts a worktree a human edited under the same contract, so
// an unchanged worktree is its correct outcome rather than work not done.
func TestAVerificationOnlyRunIsNotRefusedForChangingNothing(t *testing.T) {
	const c = "unchanged"
	green := []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
	}

	ok, reasons := task.Accept(recipe.Standard, green, c, nil,
		task.Effect{Made: false, Expected: false})

	if !ok {
		t.Fatalf("a verification-only run was refused for changing nothing: %v", reasons)
	}
}

// The exemption above is only correct if the verify engine actually declares
// itself non-editing; if that link breaks, the clause silently stops applying
// to every real task instead.
func TestTheVerifyEngineDeclaresItselfNonEditing(t *testing.T) {
	if engine.Edits(engine.Verify{}) {
		t.Error("engine.Verify must report that it does not edit, or `le task verify` " +
			"on a clean worktree is refused for changing nothing")
	}
}

// An engine that says nothing about itself must be assumed to edit: that way a
// new engine can only have a no-op wrongly refused, never wrongly accepted.
func TestAnEngineThatSaysNothingIsAssumedToEdit(t *testing.T) {
	if !engine.Edits(stubEngine{}) {
		t.Error("an engine that does not declare itself must default to editing")
	}
}

type stubEngine struct{ engine.Engine }
