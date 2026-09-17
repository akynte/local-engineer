package eval

import "math"

// Paired is the result of comparing two arms on the runs they both attempted.
//
// The arms are not independent samples. Every arm is given the same tasks, so
// the dominant source of variance — one task being harder than another — is
// shared between them and cancels when the comparison is made within a task
// rather than across the set. Comparing two Wilson intervals throws that away:
// it asks whether two independently-drawn rates differ, which is a question
// about a design this evaluation does not have, and answers "cannot tell"
// long after the data could tell.
//
// The test is McNemar's, exact rather than chi-squared because the discordant
// counts here are single digits. Concordant pairs — both arms solved it, or
// neither did — carry no information about which is better and are excluded by
// construction; only the pairs where the arms disagree can move the verdict.
type Paired struct {
	// VariantOnly counts pairs the variant solved and the baseline did not.
	VariantOnly int `json:"variant_only"`
	// BaselineOnly counts pairs the baseline solved and the variant did not.
	BaselineOnly int `json:"baseline_only"`
	// Both and Neither are the concordant pairs. They are reported because a
	// comparison resting on four discordant pairs out of fifty should look
	// thin to a reader, and a bare p-value hides that.
	Both    int `json:"both"`
	Neither int `json:"neither"`
	// P is the two-sided exact probability of a split at least this lopsided
	// when the arms are equally good.
	P float64 `json:"p"`
}

// Discordant is the number of pairs that carry any information.
func (p Paired) Discordant() int { return p.VariantOnly + p.BaselineOnly }

// Pairs is how many runs the two arms both attempted.
func (p Paired) Pairs() int { return p.Discordant() + p.Both + p.Neither }

// Decided reports whether the difference is distinguishable from chance at the
// conventional level. It is a sharper instrument than overlapping intervals,
// so it is held to a stated threshold rather than to a visual one.
func (p Paired) Decided() bool { return p.Discordant() > 0 && p.P < 0.05 }

// pairKey identifies one run of one task, so the same task on the same pass is
// compared across arms rather than against a different pass of itself.
type pairKey struct {
	task       string
	repetition int
}

// pairArms runs McNemar's exact test over the runs two arms share.
//
// Errored runs are dropped from both sides of a pair: a harness fault is not
// evidence about either arm, and keeping a pair where one side never ran would
// score that fault as a loss.
func pairArms(outcomes []Outcome, baseline, variant string) Paired {
	solved := map[pairKey]map[string]bool{}
	for _, o := range outcomes {
		if o.Errored() || (o.Arm != baseline && o.Arm != variant) {
			continue
		}
		k := pairKey{task: o.TaskID, repetition: o.Repetition}
		if solved[k] == nil {
			solved[k] = map[string]bool{}
		}
		solved[k][o.Arm] = o.Solved
	}

	var p Paired
	for _, byArm := range solved {
		b, haveBase := byArm[baseline]
		v, haveVariant := byArm[variant]
		if !haveBase || !haveVariant {
			continue
		}
		switch {
		case v && !b:
			p.VariantOnly++
		case b && !v:
			p.BaselineOnly++
		case b && v:
			p.Both++
		default:
			p.Neither++
		}
	}
	p.P = exactBinomialTwoSided(p.VariantOnly, p.Discordant())
	return p
}

// exactBinomialTwoSided is the probability of a split at least as lopsided as
// k out of n, when each discordant pair is a fair coin.
//
// With no discordant pairs there is nothing to test and the answer is 1: the
// arms produced identical verdicts everywhere, which is not evidence that they
// are the same, only that this set did not separate them.
func exactBinomialTwoSided(k, n int) float64 {
	if n == 0 {
		return 1
	}
	lo := k
	if n-k < lo {
		lo = n - k
	}
	var tail float64
	for i := 0; i <= lo; i++ {
		tail += math.Exp(logChoose(n, i) - float64(n)*math.Ln2)
	}
	if p := 2 * tail; p < 1 {
		return p
	}
	return 1
}

// logChoose keeps the binomial coefficient in log space so a large pair count
// cannot overflow it.
func logChoose(n, k int) float64 {
	lg, _ := math.Lgamma(float64(n + 1))
	lk, _ := math.Lgamma(float64(k + 1))
	lnk, _ := math.Lgamma(float64(n - k + 1))
	return lg - lk - lnk
}
