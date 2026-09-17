package eval_test

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/eval"
)

// The statistics are the part most likely to mislead. A rate quoted without an
// interval gets read as precise, and a 10-point gap between two arms over
// twenty tasks means nothing at all.

func TestWilsonIntervalIsSaneAtTheEdges(t *testing.T) {
	cases := []struct {
		successes, total int
		wantValue        float64
	}{
		{0, 10, 0.0},
		{10, 10, 1.0},
		{5, 10, 0.5},
		{6, 20, 0.3},
	}
	for _, c := range cases {
		r := eval.NewRate(c.successes, c.total)
		if math.Abs(r.Value-c.wantValue) > 1e-9 {
			t.Errorf("%d/%d: value = %f, want %f", c.successes, c.total, r.Value, c.wantValue)
		}
		// The normal approximation produces bounds outside [0,1] at these
		// edges, which is exactly the regime a twenty-task set sits in.
		if r.Low < 0 || r.High > 1 {
			t.Errorf("%d/%d: interval [%f, %f] escapes [0,1]", c.successes, c.total, r.Low, r.High)
		}
		if r.Low > r.Value || r.High < r.Value {
			t.Errorf("%d/%d: the interval does not contain the estimate", c.successes, c.total)
		}
	}

	// A perfect score still has an interval: 10/10 is not proof of 100%.
	perfect := eval.NewRate(10, 10)
	if perfect.Low >= 1.0 {
		t.Error("a perfect sample must not produce a lower bound of 1; ten successes is not certainty")
	}
	// More data narrows it.
	wide, narrow := eval.NewRate(5, 10), eval.NewRate(50, 100)
	if (narrow.High - narrow.Low) >= (wide.High - wide.Low) {
		t.Error("a larger sample must produce a narrower interval")
	}
}

func TestRateStringCarriesTheInterval(t *testing.T) {
	s := eval.NewRate(12, 20).String()
	// A bare percentage is the thing that gets quoted out of context.
	if !strings.Contains(s, "[") || !strings.Contains(s, "12/20") {
		t.Errorf("a rate must render its interval and its sample: %q", s)
	}
	if eval.NewRate(0, 0).String() != "n/a (0 runs)" {
		t.Errorf("an empty sample must say so, not print 0%%")
	}
}

// With a set this size, claiming a difference the data cannot support is the
// likeliest way these numbers mislead.
func TestOverlappingIntervalsAreNotSignificant(t *testing.T) {
	var outcomes []eval.Outcome
	// 12/20 versus 14/20 — a 10-point gap that means nothing at this size.
	outcomes = append(outcomes, runs("unsupervised", 12, 8)...)
	outcomes = append(outcomes, runs("supervised", 14, 6)...)

	rep := eval.Aggregate(outcomes, tasksFor(20))
	var found bool
	for _, c := range rep.Comparisons {
		if c.Baseline != "unsupervised" || c.Variant != "supervised" {
			continue
		}
		found = true
		if c.Significant {
			t.Errorf("a 10-point gap over 20 tasks must not be called significant: %+v", c)
		}
		if !strings.Contains(c.Verdict, "no detectable difference") {
			t.Errorf("the verdict should say it cannot tell: %q", c.Verdict)
		}
		// The reader has to be able to see how thin the evidence is, or
		// "cannot tell" is indistinguishable from "measured and equal".
		if !strings.Contains(c.Verdict, "disagreed on") {
			t.Errorf("the verdict must name the evidence it rests on: %q", c.Verdict)
		}
		if c.Paired.Discordant() != 2 {
			t.Errorf("discordant = %d, want 2", c.Paired.Discordant())
		}
	}
	if !found {
		t.Fatal("the headline comparison was not produced")
	}
}

func TestAClearDifferenceIsReported(t *testing.T) {
	var outcomes []eval.Outcome
	outcomes = append(outcomes, runs("unsupervised", 2, 58)...)
	outcomes = append(outcomes, runs("supervised", 45, 15)...)

	rep := eval.Aggregate(outcomes, tasksFor(60))
	for _, c := range rep.Comparisons {
		if c.Baseline == "unsupervised" && c.Variant == "supervised" {
			if !c.Significant {
				t.Errorf("a 72-point gap over 60 tasks should be detectable: %+v", c)
			}
			if !strings.Contains(c.Verdict, "better") {
				t.Errorf("verdict = %q", c.Verdict)
			}
		}
	}
}

// A regression must be reported as loudly as an improvement.
func TestARegressionIsCalledOut(t *testing.T) {
	var outcomes []eval.Outcome
	outcomes = append(outcomes, runs("supervised-no-graph", 45, 15)...)
	outcomes = append(outcomes, runs("supervised", 10, 50)...)

	rep := eval.Aggregate(outcomes, tasksFor(60))
	for _, c := range rep.Comparisons {
		if c.Variant == "supervised" && c.Baseline == "supervised-no-graph" {
			if !strings.Contains(c.Verdict, "WORSE") {
				t.Errorf("a regression must be stated plainly: %q", c.Verdict)
			}
		}
	}
}

// A solved rate should never be read without the false-acceptance rate beside
// it, so the caveat is generated rather than left to a writer.
func TestCaveatsAreGeneratedFromWhatWasRun(t *testing.T) {
	outcomes := []eval.Outcome{
		{Arm: "supervised", Category: eval.CategoryBugFix, Solved: true, Claimed: true},
		{Arm: "supervised", Category: eval.CategoryBugFix, Solved: false, Claimed: true, FalseAccept: true},
		{Arm: "supervised", Category: eval.CategoryBugFix, Err: "boom"},
	}
	tasks := []eval.Task{
		{ID: "a", LeakRisk: eval.LeakNone},
		{ID: "b", LeakRisk: eval.LeakPublic},
		{ID: "c", LeakRisk: eval.LeakUnknown},
	}
	rep := eval.Aggregate(outcomes, tasks)
	joined := strings.Join(rep.Caveats, "\n")

	for _, want := range []string{
		"claimed success and was wrong",
		"public sources",
		"unestablished provenance",
		"errored",
		"task set has 3 tasks",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("a required caveat is missing (%q):\n%s", want, joined)
		}
	}
}

func TestPerCategoryRatesAreSeparate(t *testing.T) {
	outcomes := []eval.Outcome{
		{Arm: "supervised", Category: eval.CategoryBugFix, Solved: true},
		{Arm: "supervised", Category: eval.CategoryBugFix, Solved: true},
		{Arm: "supervised", Category: eval.CategoryRefactor, Solved: false},
		{Arm: "supervised", Category: eval.CategoryRefactor, Solved: false},
	}
	rep := eval.Aggregate(outcomes, tasksFor(4))
	arm := rep.Arms[0]

	// Averaging a bug fix with a refactor hides which one the system is bad at.
	if got := arm.ByCategory[eval.CategoryBugFix].Value; got != 1.0 {
		t.Errorf("bug_fix rate = %f, want 1.0", got)
	}
	if got := arm.ByCategory[eval.CategoryRefactor].Value; got != 0.0 {
		t.Errorf("refactor rate = %f, want 0.0", got)
	}
	if got := arm.Solved.Value; got != 0.5 {
		t.Errorf("overall rate = %f, want 0.5", got)
	}
}

func TestFormatIncludesIntervalsAndCaveats(t *testing.T) {
	outcomes := append(runs("unsupervised", 3, 7), runs("supervised", 6, 4)...)
	rep := eval.Aggregate(outcomes, tasksFor(10))
	text := rep.Format()

	for _, want := range []string{"SOLVED", "FALSE ACCEPT", "Comparisons", "How to read these numbers", "["} {
		if !strings.Contains(text, want) {
			t.Errorf("the rendered report is missing %q:\n%s", want, text)
		}
	}
}

// runs builds n outcomes for an arm with the given solved/unsolved split.
//
// Each outcome is given a distinct task id, numbered from zero, so that two
// arms built this way pair up run for run. Without it every outcome carried
// the zero task id and the whole arm collapsed into one pair, which made every
// comparison rest on a single observation.
func runs(arm string, solved, unsolved int) []eval.Outcome {
	var out []eval.Outcome
	for i := 0; i < solved; i++ {
		out = append(out, eval.Outcome{
			TaskID: fmt.Sprintf("t%d", i),
			Arm:    arm, Category: eval.CategoryBugFix, Solved: true, Claimed: true,
			Duration: time.Second, Attempts: 1,
		})
	}
	for i := 0; i < unsolved; i++ {
		out = append(out, eval.Outcome{
			TaskID: fmt.Sprintf("t%d", solved+i),
			Arm:    arm, Category: eval.CategoryBugFix, Solved: false, Claimed: false,
			Duration: time.Second, Attempts: 1,
		})
	}
	return out
}

func tasksFor(n int) []eval.Task {
	out := make([]eval.Task, n)
	for i := range out {
		out[i] = eval.Task{ID: "t", LeakRisk: eval.LeakNone}
	}
	return out
}

// TestUnstableCellsAreReported pins the caveat that governs every other number
// in a report. A cell that comes out solved on one pass and unsolved on the
// next has not been measured, and a confidence interval computed over it
// assumes a stability the data contradicts. Two consecutive runs of the same
// task set produced exactly this, so it is reported rather than left for a
// reader to notice.
func TestUnstableCellsAreReported(t *testing.T) {
	tasks := []eval.Task{{ID: "t1", LeakRisk: eval.LeakNone}}
	outcomes := []eval.Outcome{
		{TaskID: "t1", Arm: "a", Repetition: 1, Solved: true, Claimed: true},
		{TaskID: "t1", Arm: "a", Repetition: 2, Solved: false, Claimed: true, FalseAccept: true},
	}
	rep := eval.Aggregate(outcomes, tasks)

	joined := strings.Join(rep.Caveats, " ")
	if !strings.Contains(joined, "changed verdict between passes") {
		t.Errorf("a cell that flipped verdict between passes was not reported:\n%v", rep.Caveats)
	}
	if !strings.Contains(joined, "1 of 1") {
		t.Errorf("the unstable-cell count is wrong:\n%v", rep.Caveats)
	}
}

// TestSingleRunSaysSo is the other half: a report from one pass must say that
// its cells are single samples, so the absence of an instability warning is
// never read as evidence of stability.
func TestSingleRunSaysSo(t *testing.T) {
	tasks := []eval.Task{{ID: "t1", LeakRisk: eval.LeakNone}}
	rep := eval.Aggregate([]eval.Outcome{
		{TaskID: "t1", Arm: "a", Solved: true, Claimed: true},
	}, tasks)

	if !strings.Contains(strings.Join(rep.Caveats, " "), "single run of a cell is one sample") {
		t.Errorf("a one-pass report does not say its cells are single samples:\n%v", rep.Caveats)
	}
}

// TestStableCellsAreNotFlagged keeps the warning meaningful: a set that agreed
// with itself across passes must not carry an instability caveat.
func TestStableCellsAreNotFlagged(t *testing.T) {
	tasks := []eval.Task{{ID: "t1", LeakRisk: eval.LeakNone}}
	rep := eval.Aggregate([]eval.Outcome{
		{TaskID: "t1", Arm: "a", Repetition: 1, Solved: true, Claimed: true},
		{TaskID: "t1", Arm: "a", Repetition: 2, Solved: true, Claimed: true},
	}, tasks)

	if strings.Contains(strings.Join(rep.Caveats, " "), "changed verdict between passes") {
		t.Errorf("a stable set was flagged as unstable:\n%v", rep.Caveats)
	}
}
