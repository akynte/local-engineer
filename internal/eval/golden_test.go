package eval_test

import (
	"math"
	"testing"

	"github.com/akynte/local-engineer/internal/eval"
)

// goldenSet is a hand-built run set whose every statistic was worked out on
// paper before the code ran. It exists because the failure mode of a statistics
// layer is not a crash: it is a plausible wrong number published as evidence.
//
// Three tasks, two arms, two runs each. Laid out so that every metric has a
// different value and a transposition would be caught:
//
//	task  arm         run1                run2
//	A     baseline    claimed, wrong      claimed, wrong     -> 2 false accepts
//	A     supervised  claimed, right      claimed, right     -> 2 true accepts
//	B     baseline    claimed, right      claimed, right     -> 2 true accepts
//	B     supervised  claimed, right      claimed, right     -> 2 true accepts
//	C     baseline    not claimed, wrong  not claimed, wrong -> 2 true rejects
//	C     supervised  not claimed, right  not claimed, wrong -> 1 false reject
func goldenSet() []eval.Outcome {
	mk := func(task, arm string, rep int, claimed, solved bool) eval.Outcome {
		status := eval.StatusCompleted
		if !solved {
			status = eval.StatusTaskFailed
		}
		return eval.Outcome{
			TaskID: task, Arm: arm, Repetition: rep, Claimed: claimed, Solved: solved,
			FalseAccept: claimed && !solved, MissedSuccess: !claimed && solved,
			Status: status, Set: eval.SetHeldout,
		}
	}
	return []eval.Outcome{
		mk("A", "baseline", 1, true, false), mk("A", "baseline", 2, true, false),
		mk("A", "supervised", 1, true, true), mk("A", "supervised", 2, true, true),
		mk("B", "baseline", 1, true, true), mk("B", "baseline", 2, true, true),
		mk("B", "supervised", 1, true, true), mk("B", "supervised", 2, true, true),
		mk("C", "baseline", 1, false, false), mk("C", "baseline", 2, false, false),
		mk("C", "supervised", 1, false, true), mk("C", "supervised", 2, false, false),
	}
}

func TestGoldenConfusionAndFalseAcceptance(t *testing.T) {
	out := goldenSet()

	base := eval.ConfusionOf(out, "baseline")
	// Worked by hand: A gives 2 false accepts, B gives 2 true accepts,
	// C gives 2 true rejects.
	if base.FalseAccept != 2 || base.TrueAccept != 2 || base.TrueReject != 2 || base.FalseReject != 0 {
		t.Fatalf("baseline confusion: %+v", base)
	}
	// Claims = true accepts + false accepts = 4. FAR = 2/4 = 0.5 exactly.
	if base.Claims() != 4 {
		t.Fatalf("baseline claims = %d, want 4", base.Claims())
	}
	if got := base.FalseAcceptanceRate().Value; math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("baseline false-acceptance rate = %v, want 0.5", got)
	}
	// Share uses every evidence run as the denominator: 2/6.
	if got := base.FalseAcceptanceShare().Value; math.Abs(got-2.0/6.0) > 1e-9 {
		t.Fatalf("baseline false-acceptance share = %v, want 1/3", got)
	}

	sup := eval.ConfusionOf(out, "supervised")
	// A and B give 4 true accepts; C gives one false reject and one true reject.
	if sup.TrueAccept != 4 || sup.FalseAccept != 0 || sup.FalseReject != 1 || sup.TrueReject != 1 {
		t.Fatalf("supervised confusion: %+v", sup)
	}
	// The headline the metric exists for: the supervised arm never claimed a
	// success it did not have.
	if sup.FalseAcceptanceRate().Value != 0 {
		t.Fatalf("supervised false-acceptance rate = %v, want 0", sup.FalseAcceptanceRate().Value)
	}
	if got := sup.FalseRejectionRate().Value; math.Abs(got-0.5) > 1e-9 {
		t.Fatalf("supervised false-rejection rate = %v, want 0.5", got)
	}
}

func TestGoldenHierarchicalDeltaIsPairedAndClustered(t *testing.T) {
	out := goldenSet()
	d := eval.HierarchicalCompare(out, "baseline", "supervised", "task_success",
		func(o eval.Outcome) bool { return o.Solved }, 1, 2000)

	// Per task: A = 1.0 - 0.0 = +1.0, B = 1.0 - 1.0 = 0, C = 0.5 - 0.0 = +0.5.
	// Mean over the three tasks = 0.5 exactly. Averaging over the twelve runs
	// instead would give a different number, which is the bug this pins.
	if math.Abs(d.Delta-0.5) > 1e-9 {
		t.Fatalf("paired delta = %v, want 0.5", d.Delta)
	}
	// The clustering unit is the task, not the run.
	if d.Tasks != 3 {
		t.Fatalf("tasks = %d, want 3", d.Tasks)
	}
	if d.Runs != 12 {
		t.Fatalf("runs = %d, want 12", d.Runs)
	}
	if d.Low > d.Delta || d.High < d.Delta {
		t.Fatalf("interval [%v, %v] excludes the point estimate %v", d.Low, d.High, d.Delta)
	}
}

// Three tasks cannot separate anything. The verdict must say so rather than
// reporting the absence of a finding as a finding.
func TestGoldenSmallSampleIsInsufficientNotNoEffect(t *testing.T) {
	d := eval.HierarchicalCompare(goldenSet(), "baseline", "supervised", "task_success",
		func(o eval.Outcome) bool { return o.Solved }, 1, 2000)
	if d.Verdict == eval.VerdictNoMaterialDifference {
		t.Fatal("three tasks reported as no material difference; that is absence of evidence")
	}
}

// A report has to be the same twice or it is not a report.
func TestGoldenBootstrapIsDeterministicForASeed(t *testing.T) {
	out := goldenSet()
	run := func(seed int64) eval.HierarchicalDelta {
		return eval.HierarchicalCompare(out, "baseline", "supervised", "task_success",
			func(o eval.Outcome) bool { return o.Solved }, seed, 2000)
	}
	a, b := run(7), run(7)
	if a.Low != b.Low || a.High != b.High {
		t.Fatalf("same seed gave [%v,%v] then [%v,%v]", a.Low, a.High, b.Low, b.High)
	}
}

// Seed sensitivity is checked on a wider set, because on three tasks the 2.5
// and 97.5 percentiles sit at the extremes of a handful of attainable means and
// two seeds legitimately agree. Asserting a difference there would be asserting
// a property of the fixture, not of the code.
func TestGoldenSeedChangesTheIntervalWhenTheSetCanShowIt(t *testing.T) {
	var out []eval.Outcome
	for i := range 24 {
		task := "t" + string(rune('a'+i))
		// A spread of per-task behaviour, so resampling tasks genuinely moves
		// the mean: a third clearly helped, a third unchanged, a third hurt.
		baselineSolved, variantSolved := i%3 == 0, i%3 != 2
		for rep := 1; rep <= 2; rep++ {
			out = append(out,
				eval.Outcome{TaskID: task, Arm: "baseline", Repetition: rep,
					Solved: baselineSolved, Status: eval.StatusCompleted},
				eval.Outcome{TaskID: task, Arm: "variant", Repetition: rep,
					Solved: variantSolved, Status: eval.StatusCompleted})
		}
	}
	run := func(seed int64, resamples int) eval.HierarchicalDelta {
		return eval.HierarchicalCompare(out, "baseline", "variant", "task_success",
			func(o eval.Outcome) bool { return o.Solved }, seed, resamples)
	}
	// The point estimate is a property of the data and must not move at all.
	if a, b := run(1, 2000), run(99, 2000); a.Delta != b.Delta {
		t.Fatalf("the point estimate moved with the seed: %v vs %v", a.Delta, b.Delta)
	}
	// Seed sensitivity is asserted at a low resample count, because that is
	// where it is observable. At two thousand resamples the bootstrap has
	// converged and different seeds agree — which is the behaviour wanted, so
	// asserting a difference there would fail on a correct implementation.
	low1, low2 := run(1, 50), run(2, 50)
	if low1.Low == low2.Low && low1.High == low2.High {
		t.Fatal("two seeds gave an identical interval at 50 resamples; the seed is not wired in")
	}
	// And convergence: many resamples should agree far more closely than few.
	wide := run(1, 50).High - run(1, 50).Low
	converged1, converged2 := run(1, 4000), run(2, 4000)
	if spread := absDiff(converged1.High, converged2.High); spread > wide {
		t.Fatalf("more resamples disagreed more (%v) than one small run's width (%v)", spread, wide)
	}
}

func absDiff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

// Environmental failures are not evidence in either direction.
func TestGoldenInvalidRunsAreExcludedFromDenominators(t *testing.T) {
	out := append(goldenSet(),
		eval.Outcome{TaskID: "D", Arm: "baseline", Claimed: true, Solved: false,
			Status: eval.StatusEnvironmentFailed, Err: "disk full"},
		eval.Outcome{TaskID: "D", Arm: "baseline", Claimed: true, Solved: false,
			Status: eval.StatusInvalid, Err: "hidden test reachable"},
	)
	base := eval.ConfusionOf(out, "baseline")
	if base.Total() != 6 {
		t.Fatalf("evidence runs = %d, want 6: a crashed harness is not a false claim", base.Total())
	}
	// They must still be visible.
	counts := eval.StatusCounts(out, "baseline")
	if counts[eval.StatusEnvironmentFailed] != 1 || counts[eval.StatusInvalid] != 1 {
		t.Fatalf("excluded runs vanished from the report: %v", counts)
	}
}

// Results written before statuses existed must keep their meaning.
func TestGoldenLegacyOutcomesWithoutStatusStillCount(t *testing.T) {
	legacy := []eval.Outcome{
		{TaskID: "A", Arm: "baseline", Claimed: true, Solved: false},
		{TaskID: "B", Arm: "baseline", Claimed: true, Solved: true},
		{TaskID: "C", Arm: "baseline", Claimed: true, Solved: false, Err: "harness fault"},
	}
	c := eval.ConfusionOf(legacy, "baseline")
	if c.Total() != 2 {
		t.Fatalf("legacy evidence runs = %d, want 2 (the errored one excluded)", c.Total())
	}
	if c.FalseAccept != 1 {
		t.Fatalf("legacy false accepts = %d, want 1", c.FalseAccept)
	}
	errored := eval.Outcome{Err: "x"}
	if got := errored.Classified(); got != eval.StatusEnvironmentFailed {
		t.Fatalf("legacy errored run classified as %q", got)
	}
}
