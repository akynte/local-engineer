package eval

import (
	"math"
	"math/rand"
	"sort"
)

// HierarchicalDelta is a paired difference between two arms with an interval
// that respects how the runs were actually collected.
//
// Runs are nested inside tasks: five passes over one task are five looks at the
// same problem, not five problems. Resampling runs directly treats them as
// independent and produces an interval far narrower than the evidence supports
// — which is how a benchmark ends up claiming a difference it has not earned.
// This resamples tasks first, then resamples the runs within each drawn task,
// so the dominant variance (which tasks you happened to pick) is carried
// through to the interval.
type HierarchicalDelta struct {
	// Metric names what was differenced, so a reader is never guessing.
	Metric string `json:"metric"`
	// Baseline and Variant are the arms, variant minus baseline.
	Baseline string `json:"baseline"`
	Variant  string `json:"variant"`

	// Tasks is the clustering unit and the real sample size. Runs is how many
	// observations went in. A report that shows only Runs invites the reader
	// to believe the sample is larger than it is.
	Tasks int `json:"tasks"`
	Runs  int `json:"runs"`

	// Delta is the observed paired difference, variant minus baseline, in
	// proportion points (0.1 = ten points).
	Delta float64 `json:"delta"`
	// Low and High bound it at 95%.
	Low  float64 `json:"ci_low"`
	High float64 `json:"ci_high"`

	// Resamples and Seed make the interval reproducible.
	Resamples int   `json:"resamples"`
	Seed      int64 `json:"seed"`

	// Verdict is the claim this difference does and does not support.
	Verdict Verdict `json:"verdict"`
}

// Verdict separates the four things a comparison can mean.
//
// The category that is usually missing is the last one, and collapsing it into
// "no difference" is the most common way a benchmark lies: a sample too small
// to detect an effect is not a sample that found its absence.
type Verdict string

const (
	// VerdictPositive: the interval excludes zero on the improving side.
	VerdictPositive Verdict = "positive_evidence"
	// VerdictNegative: the interval excludes zero on the degrading side.
	VerdictNegative Verdict = "negative_evidence"
	// VerdictNoMaterialDifference: the interval contains zero and is tight
	// enough that a difference worth acting on would have shown.
	VerdictNoMaterialDifference Verdict = "no_material_difference"
	// VerdictInsufficient: the interval contains zero and is too wide to say
	// anything. Not the same as no difference.
	VerdictInsufficient Verdict = "insufficient_evidence"
)

// materialWidth is the interval width above which a zero-containing result is
// reported as insufficient rather than as no difference.
//
// Twenty points: an interval wider than that on a success rate cannot rule out
// an effect anyone would care about, so calling it "no difference" would be an
// overstatement of what the data supports.
const materialWidth = 0.20

// defaultResamples is enough for a stable 95% interval without making a report
// slow to generate.
const defaultResamples = 10000

// taskRuns groups one arm's runs by task.
type taskRuns map[string][]bool

// HierarchicalCompare differences a binary metric between two arms.
//
// metric selects what counts as a one for each run; only runs whose status is
// evidence are considered, and a task contributes only if both arms have at
// least one usable run on it. Everything else is dropped with the pair, because
// half a pair is not a comparison.
func HierarchicalCompare(outcomes []Outcome, baseline, variant, metric string,
	value func(Outcome) bool, seed int64, resamples int) HierarchicalDelta {

	if resamples <= 0 {
		resamples = defaultResamples
	}
	d := HierarchicalDelta{Metric: metric, Baseline: baseline, Variant: variant,
		Resamples: resamples, Seed: seed, Verdict: VerdictInsufficient}

	base, vary := taskRuns{}, taskRuns{}
	for _, o := range outcomes {
		if !o.EvidenceRun() {
			continue
		}
		switch o.Arm {
		case baseline:
			base[o.TaskID] = append(base[o.TaskID], value(o))
		case variant:
			vary[o.TaskID] = append(vary[o.TaskID], value(o))
		}
	}

	var tasks []string
	for id := range base {
		if len(vary[id]) > 0 {
			tasks = append(tasks, id)
		}
	}
	sort.Strings(tasks)
	if len(tasks) == 0 {
		return d
	}
	d.Tasks = len(tasks)
	for _, id := range tasks {
		d.Runs += len(base[id]) + len(vary[id])
	}
	d.Delta = pairedMean(tasks, base, vary)

	// Resample tasks, then runs within each drawn task.
	rng := rand.New(rand.NewSource(seed)) //nolint:gosec // reproducibility, not secrecy
	deltas := make([]float64, 0, resamples)
	for i := 0; i < resamples; i++ {
		drawnBase, drawnVary := taskRuns{}, taskRuns{}
		drawn := make([]string, 0, len(tasks))
		for j := 0; j < len(tasks); j++ {
			id := tasks[rng.Intn(len(tasks))]
			// A task drawn twice must contribute twice, so it is keyed by
			// draw rather than by name.
			key := id + "#" + string(rune('a'+j%26)) + itoa(j)
			drawn = append(drawn, key)
			drawnBase[key] = resampleRuns(base[id], rng)
			drawnVary[key] = resampleRuns(vary[id], rng)
		}
		deltas = append(deltas, pairedMean(drawn, drawnBase, drawnVary))
	}
	sort.Float64s(deltas)
	d.Low, d.High = percentile(deltas, 0.025), percentile(deltas, 0.975)
	d.Verdict = verdictFromInterval(d.Low, d.High)
	return d
}

// pairedMean is the mean over tasks of (variant rate - baseline rate) within
// that task. Averaging within a task first is what makes a task with ten runs
// count once rather than ten times.
func pairedMean(tasks []string, base, vary taskRuns) float64 {
	var sum float64
	var n int
	for _, id := range tasks {
		b, v := base[id], vary[id]
		if len(b) == 0 || len(v) == 0 {
			continue
		}
		sum += rateOf(v) - rateOf(b)
		n++
	}
	if n == 0 {
		return 0
	}
	return sum / float64(n)
}

func resampleRuns(runs []bool, rng *rand.Rand) []bool {
	if len(runs) == 0 {
		return nil
	}
	out := make([]bool, len(runs))
	for i := range out {
		out[i] = runs[rng.Intn(len(runs))]
	}
	return out
}

// rateOf is the proportion of true runs, the within-task summary the paired
// difference is taken over.
func rateOf(runs []bool) float64 {
	if len(runs) == 0 {
		return 0
	}
	var n int
	for _, r := range runs {
		if r {
			n++
		}
	}
	return float64(n) / float64(len(runs))
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(math.Round(p * float64(len(sorted)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

func verdictFromInterval(low, high float64) Verdict {
	switch {
	case low > 0:
		return VerdictPositive
	case high < 0:
		return VerdictNegative
	case high-low <= materialWidth:
		return VerdictNoMaterialDifference
	default:
		return VerdictInsufficient
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
