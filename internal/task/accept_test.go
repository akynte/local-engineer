package task_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

// The completion contract is the rule the whole design rests on, so it is
// tested rule by rule rather than through a happy path.

func pass(kind recipe.Kind, candidate string) recipe.Result {
	return recipe.Result{
		Recipe: string(kind), Kind: kind, Status: recipe.Pass, Candidate: candidate,
		Summary: recipe.Summary{Headline: "ok"},
	}
}

func fail(kind recipe.Kind, candidate, headline string) recipe.Result {
	return recipe.Result{
		Recipe: string(kind), Kind: kind, Status: recipe.Fail, Candidate: candidate,
		Summary: recipe.Summary{Headline: headline},
	}
}

func TestStandardLevelRequiresBuildVetTestAndFormat(t *testing.T) {
	const c = "cand-1"
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
		pass(recipe.KindFormat, c),
	}, c, nil, task.Effect{Made: true, Expected: true})

	if !ok {
		t.Fatalf("expected acceptance, got: %v", reasons)
	}
	// "Why did this pass" must be as answerable as "why did it fail".
	if len(reasons) != 4 {
		t.Errorf("acceptance must explain itself, got %v", reasons)
	}
}

// CI runs `make fmt-check` on every change, so a contract that accepts
// unformatted code hands someone an acceptance that CI then overturns. A task
// accepted at standard merged a test file with its imports out of order, which
// is what put this rule at this level.
func TestStandardRefusesWhenFormattingWasNotChecked(t *testing.T) {
	const c = "cand-1"
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("standard accepted a change whose formatting was never checked")
	}
	var named bool
	for _, r := range reasons {
		if strings.Contains(r, "format") {
			named = true
		}
	}
	if !named {
		t.Errorf("the refusal must name the missing check, got %v", reasons)
	}
}

func TestAFailingRecipeBlocksAcceptance(t *testing.T) {
	const c = "cand-1"
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c),
		fail(recipe.KindTest, c, "2 test(s) failed in 1 package(s)"),
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a failing test must block acceptance")
	}
	if !containsSubstr(reasons, "2 test(s) failed") {
		t.Errorf("the reason must carry the summary: %v", reasons)
	}
}

// A recipe that did not run satisfies nothing. This is the difference between
// "the tests pass" and "we did not look".
func TestAMissingRecipeIsNotAPass(t *testing.T) {
	const c = "cand-1"
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c),
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a level requiring tests must not accept a run where tests never happened")
	}
	if !containsSubstr(reasons, "did not run") {
		t.Errorf("reasons = %v", reasons)
	}
}

func TestASkippedRecipeIsNotAPass(t *testing.T) {
	const c = "cand-1"
	skipped := recipe.Result{
		Kind: recipe.KindTest, Status: recipe.Skipped, Candidate: c,
		Summary: recipe.Summary{Headline: "skipped: the code does not compile"},
	}
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), skipped,
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a skipped recipe must not satisfy a requirement")
	}
	if !containsSubstr(reasons, "skipped") {
		t.Errorf("reasons = %v", reasons)
	}
}

// An error is a failure of the run, not a verdict on the code — and it must
// not be treated as a pass either.
func TestAnErroredRecipeIsNotAPass(t *testing.T) {
	const c = "cand-1"
	errored := recipe.Result{
		Kind: recipe.KindTest, Status: recipe.Error, Candidate: c,
		Err: "go test could not run: executable file not found",
	}
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), errored,
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a recipe that could not run must not satisfy a requirement")
	}
	if !containsSubstr(reasons, "could not run") {
		t.Errorf("reasons = %v", reasons)
	}
}

// The single most important rule: evidence describes one exact state of the
// code. A pass against an older candidate proves nothing about the current one.
func TestStaleEvidenceCannotAcceptATask(t *testing.T) {
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, "cand-2"),
		pass(recipe.KindVet, "cand-2"),
		pass(recipe.KindTest, "cand-1"), // produced before the last edit
	}, "cand-2", nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a pass against an older candidate must not accept the current one")
	}
	if !containsSubstr(reasons, "older candidate") {
		t.Errorf("the reason must name the staleness: %v", reasons)
	}
}

func TestOutOfScopeWritesBlockAcceptance(t *testing.T) {
	const c = "cand-1"
	ok, reasons := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c),
	}, c, []string{"internal/store/store.go", ".github/workflows/ci.yml"}, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("changes outside the declared scope must block acceptance")
	}
	if !containsSubstr(reasons, "outside the task's declared scope") {
		t.Errorf("reasons = %v", reasons)
	}
	if !containsSubstr(reasons, ".github/workflows/ci.yml") {
		t.Errorf("the offending files must be named: %v", reasons)
	}
}

// One passing package does not excuse a failing one.
func TestTheWorstResultPerKindDecides(t *testing.T) {
	const c = "cand-1"
	ok, _ := task.Accept(recipe.Standard, []recipe.Result{
		pass(recipe.KindBuild, c), pass(recipe.KindVet, c),
		pass(recipe.KindTest, c),
		fail(recipe.KindTest, c, "1 test failed"),
	}, c, nil, task.Effect{Made: true, Expected: true})

	if ok {
		t.Fatal("a failing result must win over a passing one of the same kind")
	}
}

func TestLowLevelOnlyRequiresCompilation(t *testing.T) {
	const c = "cand-1"
	ok, _ := task.Accept(recipe.Low, []recipe.Result{pass(recipe.KindBuild, c)}, c, nil, task.Effect{Made: true, Expected: true})
	if !ok {
		t.Fatal("the low level should accept on a passing build alone")
	}
	// But it still must not accept a failing build.
	if ok, _ := task.Accept(recipe.Low, []recipe.Result{fail(recipe.KindBuild, c, "broken")}, c, nil, task.Effect{Made: true, Expected: true}); ok {
		t.Fatal("even the low level requires the code to compile")
	}
}

func TestHighLevelRequiresRaceAndFormat(t *testing.T) {
	const c = "cand-1"
	standard := []recipe.Result{pass(recipe.KindBuild, c), pass(recipe.KindVet, c), pass(recipe.KindTest, c)}

	if ok, reasons := task.Accept(recipe.High, standard, c, nil, task.Effect{Made: true, Expected: true}); ok {
		t.Fatalf("the high level must require more than standard: %v", reasons)
	}
	full := append(standard, pass(recipe.KindRace, c), pass(recipe.KindFormat, c))
	if ok, reasons := task.Accept(recipe.High, full, c, nil, task.Effect{Made: true, Expected: true}); !ok {
		t.Fatalf("expected acceptance at the high level: %v", reasons)
	}
}

// An empty result set must never accept anything.
func TestNoEvidenceNeverAccepts(t *testing.T) {
	if ok, _ := task.Accept(recipe.Standard, nil, "c", nil, task.Effect{Made: true, Expected: true}); ok {
		t.Fatal("a task with no evidence at all must not be accepted")
	}
	if ok, _ := task.Accept(recipe.Low, nil, "c", nil, task.Effect{Made: true, Expected: true}); ok {
		t.Fatal("even the low level requires evidence")
	}
}

func containsSubstr(reasons []string, want string) bool {
	for _, r := range reasons {
		if strings.Contains(r, want) {
			return true
		}
	}
	return false
}
