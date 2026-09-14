package eval

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Aggregation is where a set of runs becomes a number someone might act on,
// so the arithmetic is deliberately conservative.
//
// A rate reported without an interval invites being read as precise. With
// twenty tasks, a 60% success rate has a 95% interval of roughly 39%–78% —
// wide enough that a 10-point difference between two arms means nothing. The
// report prints the interval next to every rate, and Significant() refuses to
// call a difference real when the intervals overlap.

// Rate is a proportion with a confidence interval and the sample it came from.
type Rate struct {
	Successes int     `json:"successes"`
	Total     int     `json:"total"`
	Value     float64 `json:"value"`
	Low       float64 `json:"ci_low"`
	High      float64 `json:"ci_high"`
}

// NewRate computes a rate and its 95% Wilson score interval.
//
// Wilson rather than the normal approximation: at small samples and rates near
// 0 or 1 the normal interval produces bounds outside [0,1] and is badly
// miscalibrated, which is exactly the regime a task set of twenty cases sits in.
func NewRate(successes, total int) Rate {
	r := Rate{Successes: successes, Total: total}
	if total <= 0 {
		return r
	}
	p := float64(successes) / float64(total)
	r.Value = p

	const z = 1.96 // 95%
	n := float64(total)
	denom := 1 + z*z/n
	centre := (p + z*z/(2*n)) / denom
	margin := z * math.Sqrt(p*(1-p)/n+z*z/(4*n*n)) / denom

	r.Low = math.Max(0, centre-margin)
	r.High = math.Min(1, centre+margin)
	return r
}

// String renders a rate with its interval, because a bare percentage is the
// thing that gets quoted out of context.
func (r Rate) String() string {
	if r.Total == 0 {
		return "n/a (0 runs)"
	}
	return fmt.Sprintf("%.0f%% [%.0f–%.0f%%] (%d/%d)",
		r.Value*100, r.Low*100, r.High*100, r.Successes, r.Total)
}

// Overlaps reports whether two intervals overlap, which is the weakest useful
// test for "these might be the same".
func (r Rate) Overlaps(other Rate) bool {
	return r.Low <= other.High && other.Low <= r.High
}

// ArmResult aggregates one arm's runs.
type ArmResult struct {
	Arm string `json:"arm"`
	// Solved is the headline: the hidden acceptance command passed.
	Solved Rate `json:"solved"`
	// FalseAccept is how often the system claimed success and was wrong. A
	// high solved rate with a high false-accept rate is not a good result.
	FalseAccept Rate `json:"false_accept"`
	// MissedSuccess is how often the work was right but the contract rejected
	// it.
	MissedSuccess Rate `json:"missed_success"`
	// Tampered is how often a solution changed something it was told not to.
	Tampered Rate `json:"tampered"`
	// Errored counts runs that could not complete. They are excluded from
	// every rate above: a harness fault is not evidence about the system.
	Errored int `json:"errored"`

	MedianDuration time.Duration `json:"median_duration"`
	MedianAttempts int           `json:"median_attempts"`
	TotalTokens    int           `json:"total_tokens,omitempty"`

	ByCategory map[Category]Rate `json:"by_category"`
}

// Report is a complete evaluation result.
type Report struct {
	// Arms holds each arm's aggregate, in the order they were run.
	Arms []ArmResult `json:"arms"`
	// Comparisons answers the questions the arm set was designed around.
	Comparisons []ComparisonResult `json:"comparisons"`
	// TaskCount and LeakMix describe the set the numbers came from. A result
	// without them is not interpretable.
	TaskCount int              `json:"task_count"`
	LeakMix   map[LeakRisk]int `json:"leak_mix"`
	// Caveats are the things a reader must know to read the numbers
	// correctly. They are generated, not written, so they cannot drift from
	// what was actually run.
	Caveats  []string  `json:"caveats"`
	RanAt    time.Time `json:"ran_at"`
	Outcomes []Outcome `json:"outcomes"`
}

// ComparisonResult is one arm-versus-arm answer.
type ComparisonResult struct {
	Question       string  `json:"question"`
	Baseline       string  `json:"baseline"`
	Variant        string  `json:"variant"`
	WhatItIsolates string  `json:"what_it_isolates"`
	BaselineRate   Rate    `json:"baseline_rate"`
	VariantRate    Rate    `json:"variant_rate"`
	Delta          float64 `json:"delta"`
	// Significant is false whenever the intervals overlap. It is deliberately
	// a weak test: with a set this size, claiming a difference the data cannot
	// support is the likeliest way these numbers mislead.
	Significant bool   `json:"significant"`
	Verdict     string `json:"verdict"`
}

// Aggregate turns outcomes into a report.
func Aggregate(outcomes []Outcome, tasks []Task) Report {
	rep := Report{
		TaskCount: len(tasks),
		LeakMix:   map[LeakRisk]int{},
		RanAt:     time.Now().UTC(),
		Outcomes:  outcomes,
	}
	for _, t := range tasks {
		rep.LeakMix[t.LeakRisk]++
	}

	byArm := map[string][]Outcome{}
	var order []string
	for _, o := range outcomes {
		if _, seen := byArm[o.Arm]; !seen {
			order = append(order, o.Arm)
		}
		byArm[o.Arm] = append(byArm[o.Arm], o)
	}

	rates := map[string]Rate{}
	for _, arm := range order {
		res := summarise(arm, byArm[arm])
		rep.Arms = append(rep.Arms, res)
		rates[arm] = res.Solved
	}

	for _, c := range Comparisons() {
		base, haveBase := rates[c.Baseline]
		variant, haveVariant := rates[c.Variant]
		if !haveBase || !haveVariant {
			continue
		}
		cr := ComparisonResult{
			Question: c.Question, Baseline: c.Baseline, Variant: c.Variant,
			WhatItIsolates: c.WhatItIsolates,
			BaselineRate:   base, VariantRate: variant,
			Delta:       variant.Value - base.Value,
			Significant: !base.Overlaps(variant),
		}
		cr.Verdict = verdictFor(cr)
		rep.Comparisons = append(rep.Comparisons, cr)
	}

	rep.Caveats = caveats(rep)
	return rep
}

func summarise(arm string, outcomes []Outcome) ArmResult {
	res := ArmResult{Arm: arm, ByCategory: map[Category]Rate{}}

	var valid []Outcome
	for _, o := range outcomes {
		if o.Errored() {
			res.Errored++
			continue
		}
		valid = append(valid, o)
	}

	var solved, falseAccept, missed, tampered int
	var durations []time.Duration
	var attempts []int
	byCategory := map[Category][2]int{}

	for _, o := range valid {
		counts := byCategory[o.Category]
		counts[1]++
		if o.Solved {
			solved++
			counts[0]++
		}
		if o.FalseAccept {
			falseAccept++
		}
		if o.MissedSuccess {
			missed++
		}
		if o.Tampered {
			tampered++
		}
		byCategory[o.Category] = counts
		durations = append(durations, o.Duration)
		attempts = append(attempts, o.Attempts)
		res.TotalTokens += o.TokensUsed
	}

	n := len(valid)
	res.Solved = NewRate(solved, n)
	res.FalseAccept = NewRate(falseAccept, n)
	res.MissedSuccess = NewRate(missed, n)
	res.Tampered = NewRate(tampered, n)
	res.MedianDuration = medianDuration(durations)
	res.MedianAttempts = medianInt(attempts)

	for cat, counts := range byCategory {
		res.ByCategory[cat] = NewRate(counts[0], counts[1])
	}
	return res
}

func verdictFor(c ComparisonResult) string {
	switch {
	case c.BaselineRate.Total == 0 || c.VariantRate.Total == 0:
		return "not run"
	case c.Significant && c.Delta > 0:
		return fmt.Sprintf("%s is better by %.0f points", c.Variant, c.Delta*100)
	case c.Significant && c.Delta < 0:
		return fmt.Sprintf("%s is WORSE by %.0f points", c.Variant, -c.Delta*100)
	default:
		return fmt.Sprintf("no detectable difference (%.0f-point gap, intervals overlap; "+
			"this set is too small to tell)", c.Delta*100)
	}
}

// caveats are generated from what was actually run, so they cannot drift from
// the numbers they qualify.
func caveats(rep Report) []string {
	var out []string

	// Instability is reported before anything else, because it governs how
	// much weight every other number can carry. A cell that comes out solved
	// on one pass and a false accept on the next has not been measured, and a
	// confidence interval computed over it assumes a stability the data
	// contradicts.
	if passes, unstable, cells := stability(rep.Outcomes); passes > 1 && unstable > 0 {
		out = append(out, fmt.Sprintf(
			"%d of %d task/arm cells changed verdict between passes. The set was run %d times; "+
				"a cell that disagrees with itself is not evidence about an arm, and comparisons "+
				"drawn across arms are weaker than the intervals alone suggest.",
			unstable, cells, passes))
	} else if passes == 1 {
		out = append(out, "The set was run once. A single run of a cell is one sample, not a "+
			"measurement of it: re-running can change a verdict. Pass --repeat to see the spread.")
	}

	if rep.TaskCount < 30 {
		out = append(out, fmt.Sprintf(
			"The task set has %d tasks. Confidence intervals at this size are wide enough that "+
				"only large differences are detectable; treat every rate as indicative.", rep.TaskCount))
	}
	if n := rep.LeakMix[LeakPublic]; n > 0 {
		out = append(out, fmt.Sprintf(
			"%d task(s) come from public sources. Results over those are an upper bound: the model "+
				"may have seen them in training.", n))
	}
	if n := rep.LeakMix[LeakUnknown]; n > 0 {
		out = append(out, fmt.Sprintf(
			"%d task(s) have unestablished provenance. Their contribution to these numbers cannot "+
				"be interpreted.", n))
	}
	for _, arm := range rep.Arms {
		if arm.Errored > 0 {
			out = append(out, fmt.Sprintf(
				"%s: %d run(s) errored and are excluded from its rates. A harness fault is not "+
					"evidence about the system, but a high count means the numbers cover fewer tasks "+
					"than the set size suggests.", arm.Arm, arm.Errored))
		}
		if arm.FalseAccept.Successes > 0 {
			out = append(out, fmt.Sprintf(
				"%s claimed success and was wrong on %d run(s) (%s). A solved rate should never be "+
					"read without this number beside it.",
				arm.Arm, arm.FalseAccept.Successes, arm.FalseAccept))
		}
		if arm.Tampered.Successes > 0 {
			out = append(out, fmt.Sprintf(
				"%s changed protected files on %d run(s); those are counted as unsolved.",
				arm.Arm, arm.Tampered.Successes))
		}
	}
	return out
}

// Format renders a report for a terminal and for docs/benchmarks/results/.
func (r Report) Format() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Evaluation — %d tasks, %d arms, %s\n\n",
		r.TaskCount, len(r.Arms), r.RanAt.Format(time.RFC3339))

	fmt.Fprintf(&b, "%-28s %-22s %-22s %s\n", "ARM", "SOLVED", "FALSE ACCEPT", "MEDIAN")
	for _, arm := range r.Arms {
		fmt.Fprintf(&b, "%-28s %-22s %-22s %s\n",
			arm.Arm, arm.Solved.String(), arm.FalseAccept.String(),
			arm.MedianDuration.Round(time.Second))
	}

	if len(r.Comparisons) > 0 {
		b.WriteString("\nComparisons\n")
		for _, c := range r.Comparisons {
			fmt.Fprintf(&b, "\n  %s\n", c.Question)
			fmt.Fprintf(&b, "    %s: %s\n", c.Baseline, c.BaselineRate)
			fmt.Fprintf(&b, "    %s: %s\n", c.Variant, c.VariantRate)
			fmt.Fprintf(&b, "    → %s\n", c.Verdict)
			fmt.Fprintf(&b, "      isolates: %s\n", c.WhatItIsolates)
		}
	}

	byCategory := map[Category]bool{}
	for _, arm := range r.Arms {
		for cat := range arm.ByCategory {
			byCategory[cat] = true
		}
	}
	if len(byCategory) > 0 {
		b.WriteString("\nBy category\n")
		cats := make([]Category, 0, len(byCategory))
		for c := range byCategory {
			cats = append(cats, c)
		}
		sort.Slice(cats, func(i, j int) bool { return cats[i] < cats[j] })
		for _, arm := range r.Arms {
			fmt.Fprintf(&b, "\n  %s\n", arm.Arm)
			for _, cat := range cats {
				if rate, ok := arm.ByCategory[cat]; ok {
					fmt.Fprintf(&b, "    %-16s %s\n", cat, rate)
				}
			}
		}
	}

	if len(r.Caveats) > 0 {
		b.WriteString("\nHow to read these numbers\n")
		for _, c := range r.Caveats {
			for i, line := range wrap(c, 74) {
				if i == 0 {
					fmt.Fprintf(&b, "  - %s\n", line)
					continue
				}
				fmt.Fprintf(&b, "    %s\n", line)
			}
		}
	}
	return b.String()
}

func medianDuration(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	sort.Slice(d, func(i, j int) bool { return d[i] < d[j] })
	return d[len(d)/2]
}

func medianInt(v []int) int {
	if len(v) == 0 {
		return 0
	}
	sort.Ints(v)
	return v[len(v)/2]
}

func wrap(s string, width int) []string {
	var lines []string
	var cur string
	for _, word := range strings.Fields(s) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) > width:
			lines = append(lines, cur)
			cur = word
		default:
			cur += " " + word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// Save writes a report as JSON.
//
// It lives here rather than in the CLI because a report knows its own format,
// and because §2.3 confines file writes to the packages allowed to perform
// them — of which this is one, for evaluation output.
func (r Report) Save(path string) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return fmt.Errorf("eval: create %s: %w", dir, err)
		}
	}
	body, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o640) //nolint:gosec // an operator-chosen output path
}

// LoadReport reads a saved report.
func LoadReport(path string) (Report, error) {
	body, err := os.ReadFile(path) //nolint:gosec // an operator-supplied results file
	if err != nil {
		return Report{}, err
	}
	var r Report
	return r, json.Unmarshal(body, &r)
}

// stability reports how many task/arm cells did not agree with themselves
// across passes, and how many passes there were.
func stability(outcomes []Outcome) (passes, unstable, cells int) {
	type cell struct{ task, arm string }
	seen := map[cell]map[bool]int{}
	for _, o := range outcomes {
		if o.Errored() {
			continue
		}
		if o.Repetition > passes {
			passes = o.Repetition
		}
		c := cell{o.TaskID, o.Arm}
		if seen[c] == nil {
			seen[c] = map[bool]int{}
		}
		seen[c][o.Solved]++
	}
	if passes == 0 {
		passes = 1
	}
	for _, verdicts := range seen {
		cells++
		if len(verdicts) > 1 {
			unstable++
		}
	}
	return passes, unstable, cells
}
