package task_test

import (
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

func passResult(kind recipe.Kind, candidate string) recipe.Result {
	return recipe.Result{
		Recipe: string(kind), Kind: kind, Status: recipe.Pass, Candidate: candidate,
		Summary: recipe.Summary{Headline: "ok"},
	}
}

func failResult(kind recipe.Kind, candidate string) recipe.Result {
	return recipe.Result{
		Recipe: string(kind), Kind: kind, Status: recipe.Fail, Candidate: candidate,
		Summary: recipe.Summary{Headline: "failed"},
	}
}

func standardPass(candidate string) []recipe.Result {
	return []recipe.Result{
		passResult(recipe.KindBuild, candidate),
		passResult(recipe.KindVet, candidate),
		passResult(recipe.KindTest, candidate),
	}
}

// §10.1: "evidence-judged, not model-voted". A candidate the completion
// contract would refuse cannot win, so "better" never means "less
// unacceptable".
func TestAnUnacceptableCandidateCannotWin(t *testing.T) {
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "a", Results: []recipe.Result{
			passResult(recipe.KindBuild, "a"), passResult(recipe.KindVet, "a"), failResult(recipe.KindTest, "a"),
		}},
		{Label: "B", Manifest: "b", Results: standardPass("b")},
	})
	if r.Best == nil {
		t.Fatal("a candidate satisfying the contract was not chosen")
	}
	if r.Best.Label != "B" {
		t.Errorf("chose %s; A fails its tests and cannot be better", r.Best.Label)
	}
}

func TestNoAcceptableCandidateChoosesNothing(t *testing.T) {
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "a", Results: []recipe.Result{failResult(recipe.KindBuild, "a")}},
		{Label: "B", Manifest: "b", Results: []recipe.Result{failResult(recipe.KindBuild, "b")}},
	})
	if r.Best != nil {
		t.Fatalf("chose %s when neither satisfied the contract", r.Best.Label)
	}
	if len(r.Reasons) == 0 {
		t.Error("refusing everything with no reason is not a usable answer")
	}
}

// A change that touches what it was not asked to touch is worse than one that
// does not, whatever its tests said.
func TestFewerOutOfScopeChangesWins(t *testing.T) {
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "a", Results: standardPass("a"), OutOfScope: []string{"unrelated.go"}},
		{Label: "B", Manifest: "b", Results: standardPass("b")},
	})
	if r.Best.Label != "B" {
		t.Errorf("chose %s; A changed a file outside its scope", r.Best.Label)
	}
}

// More passing kinds is more evidence.
func TestMoreEvidenceWins(t *testing.T) {
	withRace := append(standardPass("b"), passResult(recipe.KindRace, "b"))
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "a", Results: standardPass("a")},
		{Label: "B", Manifest: "b", Results: withRace},
	})
	if r.Best.Label != "B" {
		t.Errorf("chose %s; B has more passing evidence", r.Best.Label)
	}
}

// Diff size is a weak signal and must never outrank evidence.
func TestDiffSizeNeverOutranksEvidence(t *testing.T) {
	withRace := append(standardPass("b"), passResult(recipe.KindRace, "b"))
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "small", Manifest: "a", Results: standardPass("a"), DiffBytes: 10},
		{Label: "thorough", Manifest: "b", Results: withRace, DiffBytes: 5000},
	})
	if r.Best.Label != "thorough" {
		t.Errorf("chose %s; a smaller diff is not better evidence", r.Best.Label)
	}
}

// Equal evidence is worth surfacing: it usually means the objective admitted
// more than one reading, which is a fact about the request.
func TestEqualEvidenceIsReportedAsATie(t *testing.T) {
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "a", Results: standardPass("a"), DiffBytes: 200},
		{Label: "B", Manifest: "b", Results: standardPass("b"), DiffBytes: 100},
	})
	if !r.Tied {
		t.Error("two candidates with identical evidence were not reported as tied")
	}
	if r.Best.Label != "B" {
		t.Errorf("the tiebreak chose %s; the smaller diff should decide", r.Best.Label)
	}
	if !strings.Contains(strings.Join(r.Reasons, " "), "more than one reading") {
		t.Errorf("the tie does not say what a tie usually means: %v", r.Reasons)
	}
}

// Evidence produced against a different candidate is stale and cannot count.
func TestEvidenceForAnotherCandidateDoesNotCount(t *testing.T) {
	r := task.Rank(recipe.Standard, []task.Candidate{
		{Label: "A", Manifest: "current", Results: standardPass("older")},
	})
	if r.Best != nil {
		t.Error("a candidate whose evidence describes a different worktree was accepted")
	}
}
