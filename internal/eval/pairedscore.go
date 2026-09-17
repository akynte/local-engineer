package eval

import (
	"math"
	"math/rand"
	"sort"
)

// PairedScore compares two arms on how far through each task's hidden tests
// they got, rather than on whether they finished.
//
// It answers a narrower question than Solved does — "did this arm get further"
// is not "did this arm do the job" — and it is reported beside the binary test,
// never instead of it. What it buys is power. A binary outcome is one bit, so
// two arms that differ slightly need more runs to separate than this hardware
// can produce in a day; a fractional score carries several bits from the same
// run, and the paired difference of two fractions has far less variance than
// the difference of two proportions.
//
// The interval is a bootstrap over the per-pair differences, which assumes
// nothing about their distribution. Scores are bounded, discrete and skewed, so
// a normal interval would be wrong in exactly the regime this sits in.
type PairedScore struct {
	// Pairs is how many runs both arms attempted and both were graded on.
	Pairs int `json:"pairs"`
	// MeanDelta is the variant's mean score minus the baseline's, over pairs.
	MeanDelta float64 `json:"mean_delta"`
	// Low and High bound MeanDelta at 95%.
	Low  float64 `json:"low"`
	High float64 `json:"high"`
	// Recorded is false when the runs predate grading, in which case a zero
	// delta means "not measured" rather than "no difference".
	Recorded bool `json:"recorded"`
}

// Decided reports whether the interval excludes no-difference.
func (p PairedScore) Decided() bool {
	return p.Recorded && p.Pairs > 1 && (p.Low > 0 || p.High < 0)
}

// bootstrapResamples is fixed rather than tuned: enough that the interval is
// stable to the two decimals it is printed at.
const bootstrapResamples = 10000

// pairScores runs a paired bootstrap over the score differences of two arms.
//
// The seed is fixed so that rendering the same result file twice produces the
// same interval. A report whose numbers move when you read it again is not a
// report.
func pairScores(outcomes []Outcome, baseline, variant string) PairedScore {
	graded := map[pairKey]map[string]float64{}
	for _, o := range outcomes {
		if o.Errored() || (o.Arm != baseline && o.Arm != variant) {
			continue
		}
		if !o.Grade.Recorded() {
			continue
		}
		k := pairKey{task: o.TaskID, repetition: o.Repetition}
		if graded[k] == nil {
			graded[k] = map[string]float64{}
		}
		graded[k][o.Arm] = o.Grade.Score()
	}

	var deltas []float64
	for _, byArm := range graded {
		b, haveBase := byArm[baseline]
		v, haveVariant := byArm[variant]
		if haveBase && haveVariant {
			deltas = append(deltas, v-b)
		}
	}
	ps := PairedScore{Pairs: len(deltas), Recorded: len(deltas) > 0}
	if len(deltas) == 0 {
		return ps
	}
	ps.MeanDelta = mean(deltas)
	if len(deltas) < 2 {
		ps.Low, ps.High = ps.MeanDelta, ps.MeanDelta
		return ps
	}

	rng := rand.New(rand.NewSource(1)) //nolint:gosec // resampling, not cryptography
	means := make([]float64, bootstrapResamples)
	sample := make([]float64, len(deltas))
	for i := range means {
		for j := range sample {
			sample[j] = deltas[rng.Intn(len(deltas))]
		}
		means[i] = mean(sample)
	}
	sort.Float64s(means)
	ps.Low = means[int(0.025*float64(len(means)))]
	ps.High = means[int(0.975*float64(len(means)))-1]
	return ps
}

func mean(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s float64
	for _, x := range xs {
		s += x
	}
	return s / float64(len(xs))
}

// meanScore is an arm's mean graded score over the runs that were graded.
func meanScore(outcomes []Outcome) (float64, int) {
	var xs []float64
	for _, o := range outcomes {
		if !o.Errored() && o.Grade.Recorded() {
			xs = append(xs, o.Grade.Score())
		}
	}
	if len(xs) == 0 {
		return math.NaN(), 0
	}
	return mean(xs), len(xs)
}
