package eval

import (
	"math"
	"testing"
)

func run(task, arm string, rep int, solved bool) Outcome {
	return Outcome{TaskID: task, Arm: arm, Repetition: rep, Solved: solved}
}

// The comparison is made within a task, so a task both arms failed carries no
// information about which is better and must not dilute the result. Counting
// it would make every comparison look safer the more impossible tasks the set
// contains.
func TestConcordantPairsDoNotCountTowardsTheTest(t *testing.T) {
	var outcomes []Outcome
	for i := range 20 {
		outcomes = append(outcomes, run("hard", "a", i, false), run("hard", "b", i, false))
	}
	outcomes = append(outcomes, run("real", "a", 1, false), run("real", "b", 1, true))

	p := pairArms(outcomes, "a", "b")
	if p.Discordant() != 1 {
		t.Errorf("discordant = %d, want 1: twenty shared failures were counted as evidence", p.Discordant())
	}
	if p.Neither != 20 {
		t.Errorf("neither = %d, want 20", p.Neither)
	}
}

// Twelve disagreements all in one direction is what the first real run of the
// large-fixture set produced for supervised against unsupervised. It must come
// out decided; that is the result the whole evaluation exists to reach.
func TestOneSidedDisagreementIsDecided(t *testing.T) {
	var outcomes []Outcome
	for i := range 12 {
		outcomes = append(outcomes, run("t", "base", i, false), run("t", "variant", i, true))
	}
	p := pairArms(outcomes, "base", "variant")
	if !p.Decided() {
		t.Errorf("12–0 was not decided (p=%v)", p.P)
	}
	// Two-sided exact: 2 * (1/2)^12.
	if want := 2 * math.Pow(0.5, 12); math.Abs(p.P-want) > 1e-12 {
		t.Errorf("p = %v, want %v", p.P, want)
	}
}

// An even split is the definition of no evidence, however many pairs it spans.
func TestAnEvenSplitIsNotDecided(t *testing.T) {
	var outcomes []Outcome
	for i := range 10 {
		outcomes = append(outcomes, run("t", "base", i, i%2 == 0), run("t", "variant", i, i%2 == 1))
	}
	p := pairArms(outcomes, "base", "variant")
	if p.Decided() {
		t.Errorf("a 5–5 split was reported as decided (p=%v)", p.P)
	}
	if p.P != 1 {
		t.Errorf("p = %v, want 1 for a perfectly even split", p.P)
	}
}

// A harness fault is not evidence about either arm. Keeping the pair would
// score the fault as a loss for whichever arm happened to run.
func TestAnErroredRunDropsItsWholePair(t *testing.T) {
	outcomes := []Outcome{
		run("t", "base", 1, true),
		{TaskID: "t", Arm: "variant", Repetition: 1, Err: "sandbox failed"},
		run("t", "base", 2, false), run("t", "variant", 2, true),
	}
	p := pairArms(outcomes, "base", "variant")
	if p.Pairs() != 1 {
		t.Errorf("pairs = %d, want 1: an errored run was compared against a real one", p.Pairs())
	}
	if p.VariantOnly != 1 || p.BaselineOnly != 0 {
		t.Errorf("got %d–%d, want 1–0", p.VariantOnly, p.BaselineOnly)
	}
}

// The same task on a different pass is a different run. Comparing across
// passes would manufacture pairs that were never run against each other.
func TestPairsAreMatchedOnTaskAndPass(t *testing.T) {
	outcomes := []Outcome{
		run("t", "base", 1, true),
		run("t", "variant", 2, false),
	}
	p := pairArms(outcomes, "base", "variant")
	if p.Pairs() != 0 {
		t.Errorf("pairs = %d, want 0: runs from different passes were paired", p.Pairs())
	}
}

// With nothing to compare there is no evidence of sameness either, and the
// test must not report a difference.
func TestNoSharedRunsIsNotEvidence(t *testing.T) {
	p := pairArms([]Outcome{run("t", "base", 1, true)}, "base", "variant")
	if p.Decided() || p.P != 1 {
		t.Errorf("empty comparison reported decided=%v p=%v", p.Decided(), p.P)
	}
}
