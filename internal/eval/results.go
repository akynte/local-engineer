package eval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Results is the generated summary: everything a reader needs to judge the
// numbers, and nothing a human typed.
//
// The separation matters. A results document written by hand is a claim about
// measurements; this one is a rendering of them. If a figure here is wrong, the
// run that produced it is wrong, and the raw file it came from is named so the
// reader can go and check.
type Results struct {
	ReportSchemaVersion int       `json:"report_schema_version"`
	GeneratedAt         time.Time `json:"generated_at"`
	// Provenance labels where the runs came from, for example that they are
	// pilot data from an earlier configuration. It is rendered before any
	// number, because a reader who learns the provenance afterwards has
	// already formed an impression the label cannot undo.
	Provenance    string         `json:"provenance,omitempty"`
	Environment   Environment    `json:"environment"`
	TaskSet       TaskSetSummary `json:"task_set"`
	Arms          []ArmSummary   `json:"arms"`
	Paired        []PairedDelta  `json:"paired"`
	Ladder        []RungResult   `json:"ladder"`
	Retention     []Retention    `json:"retention"`
	Qualification Qualification  `json:"qualification"`
}

// TaskSetSummary describes what was measured, before any result is shown.
//
// It comes first because the composition of a task set determines what its
// numbers can mean: a rate over eight tasks, or over tasks whose fixtures the
// model may have trained on, is a different object from a rate over thirty
// held-out ones, and a reader who sees the rate first will anchor on it.
type TaskSetSummary struct {
	Total      int            `json:"total"`
	Real       int            `json:"real"`
	Synthetic  int            `json:"synthetic"`
	BySet      map[string]int `json:"by_set"`
	ByCategory map[string]int `json:"by_category"`
	ByLeakRisk map[string]int `json:"by_leak_risk"`
}

// ArmSummary is one configuration's headline numbers.
type ArmSummary struct {
	Arm          string         `json:"arm"`
	Description  string         `json:"description,omitempty"`
	EvidenceRuns int            `json:"evidence_runs"`
	Statuses     map[string]int `json:"statuses"`
	Success      Rate           `json:"task_success"`
	Confusion    Confusion      `json:"confusion"`
	FalseAccept  Rate           `json:"false_acceptance_rate"`
	FalseReject  Rate           `json:"false_rejection_rate"`
}

// PairedDelta is one comparison, computed two ways.
//
// McNemar answers whether the two arms differ at all on the same tasks; the
// clustered bootstrap answers by how much, with an interval that accounts for
// repeated runs of the same task not being independent samples. Both are shown
// because either alone invites the wrong conclusion.
type PairedDelta struct {
	Question       string            `json:"question"`
	WhatItIsolates string            `json:"what_it_isolates"`
	Baseline       string            `json:"baseline"`
	Variant        string            `json:"variant"`
	Success        HierarchicalDelta `json:"task_success"`
	FalseAccept    HierarchicalDelta `json:"false_acceptance"`
}

// RungResult is one ladder position and what it measured, or why it did not.
type RungResult struct {
	Rung
	Measured bool               `json:"measured"`
	Over     string             `json:"over,omitempty"`
	Delta    *HierarchicalDelta `json:"delta,omitempty"`
}

// BuildResults computes the whole document from artefacts.
func BuildResults(tasks []Task, outcomes []Outcome, env Environment, q Qualification, seed int64) Results {
	r := Results{
		ReportSchemaVersion: ReportSchemaVersion,
		GeneratedAt:         time.Now().UTC(),
		Environment:         env,
		TaskSet:             summarizeTasks(tasks),
		Qualification:       q,
	}
	arms := ArmsIn(outcomes)
	for _, name := range arms {
		r.Arms = append(r.Arms, summarizeArm(name, outcomes))
	}
	for _, c := range Comparisons() {
		if !has(arms, c.Baseline, c.Variant) {
			continue
		}
		r.Paired = append(r.Paired, PairedDelta{
			Question: c.Question, WhatItIsolates: c.WhatItIsolates,
			Baseline: c.Baseline, Variant: c.Variant,
			Success: HierarchicalCompare(outcomes, c.Baseline, c.Variant, "task_success",
				func(o Outcome) bool { return o.Solved }, seed, defaultResamples),
			FalseAccept: HierarchicalCompare(outcomes, c.Baseline, c.Variant, "false_acceptance",
				func(o Outcome) bool { return o.FalseAccept }, seed, defaultResamples),
		})
	}
	r.Ladder, r.Retention = buildLadder(outcomes, arms, seed)
	return r
}

func buildLadder(outcomes []Outcome, arms []string, seed int64) ([]RungResult, []Retention) {
	var rungs []RungResult
	var retention []Retention
	var lower string
	for _, rung := range Ladder() {
		res := RungResult{Rung: rung}
		if rung.Wired && lower != "" && has(arms, lower, rung.Arm) {
			d := HierarchicalCompare(outcomes, lower, rung.Arm, "task_success",
				func(o Outcome) bool { return o.Solved }, seed, defaultResamples)
			res.Measured, res.Over, res.Delta = true, lower, &d
			retention = append(retention, Decide(rung.Adds, d, DefaultRetention()))
		}
		rungs = append(rungs, res)
		if rung.Wired {
			lower = rung.Arm
		}
	}
	return rungs, retention
}

func summarizeTasks(tasks []Task) TaskSetSummary {
	s := TaskSetSummary{
		Total:      len(tasks),
		BySet:      map[string]int{},
		ByCategory: map[string]int{},
		ByLeakRisk: map[string]int{},
	}
	for _, t := range tasks {
		if t.Synthetic() {
			s.Synthetic++
		} else {
			s.Real++
		}
		s.BySet[string(t.Membership())]++
		s.ByCategory[string(t.Category)]++
		s.ByLeakRisk[string(t.LeakRisk)]++
	}
	return s
}

func summarizeArm(name string, outcomes []Outcome) ArmSummary {
	a := ArmSummary{Arm: name, Statuses: map[string]int{}}
	if arm, err := ArmByName(name); err == nil {
		a.Description = arm.Description
	}
	for status, n := range StatusCounts(outcomes, name) {
		a.Statuses[string(status)] = n
	}
	var solved int
	for _, o := range outcomes {
		if o.Arm != name || !o.EvidenceRun() {
			continue
		}
		a.EvidenceRuns++
		if o.Solved {
			solved++
		}
	}
	a.Success = NewRate(solved, a.EvidenceRuns)
	a.Confusion = ConfusionOf(outcomes, name)
	a.FalseAccept = a.Confusion.FalseAcceptanceRate()
	a.FalseReject = a.Confusion.FalseRejectionRate()
	return a
}

// JSON is the machine-readable summary.
func (r Results) JSON() ([]byte, error) { return json.MarshalIndent(r, "", "  ") }

// Markdown renders the published results document.
//
// The order is deliberate: what was measured, then how well it can be measured,
// then the numbers, then what they do not support. A reader who stops halfway
// should stop having learned a caveat, not having learned a headline.
func (r Results) Markdown() string {
	var b strings.Builder
	b.WriteString("# Benchmark results\n\n")
	b.WriteString("<!-- Generated by `le eval results`. Do not edit: the next run overwrites it. -->\n\n")
	fmt.Fprintf(&b, "Generated %s.\n\n", r.GeneratedAt.Format(time.RFC3339))
	if r.Provenance != "" {
		fmt.Fprintf(&b, "> **%s**\n\n", r.Provenance)
	}

	fmt.Fprintf(&b, "## Status: %s\n\n", strings.ToUpper(r.Qualification.Status))
	fmt.Fprintf(&b, "%s\n\n", r.Qualification.Recommendation)

	b.WriteString("## What was measured\n\n")
	fmt.Fprintf(&b, "%d task(s): %d real, %d synthetic.\n\n", r.TaskSet.Total, r.TaskSet.Real, r.TaskSet.Synthetic)
	writeCounts(&b, "Set", r.TaskSet.BySet)
	writeCounts(&b, "Category", r.TaskSet.ByCategory)
	writeCounts(&b, "Leak risk", r.TaskSet.ByLeakRisk)

	b.WriteString("## Environment\n\n")
	if missing := r.Environment.Missing(); len(missing) > 0 {
		fmt.Fprintf(&b, "**Incomplete.** Missing: %s. These numbers cannot be reproduced as recorded.\n\n",
			strings.Join(missing, ", "))
	}
	b.WriteString("| Field | Value |\n| --- | --- |\n")
	for _, row := range r.Environment.Rows() {
		fmt.Fprintf(&b, "| %s | %s |\n", row[0], row[1])
	}
	b.WriteString("\n")

	b.WriteString("## Per-arm results\n\n")
	b.WriteString("Intervals are Wilson score intervals over evidence runs. " +
		"They treat repeated runs of one task as independent, which they are not, " +
		"so they are narrower than the truth; the paired section below does not make that assumption.\n\n")
	b.WriteString("| Arm | Evidence runs | Task success | False acceptance | Missed success |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")
	for _, a := range r.Arms {
		fmt.Fprintf(&b, "| `%s` | %d | %s | %s | %s |\n", a.Arm, a.EvidenceRuns,
			formatRate(a.Success), formatRate(a.FalseAccept), formatRate(a.FalseReject))
	}
	b.WriteString("\nFalse acceptance is the share of runs the system *claimed* were finished that were not. " +
		"Task success is decided only by a hidden acceptance command; the system's own claim has no effect on it.\n\n")

	if len(r.Paired) > 0 {
		b.WriteString("## Paired comparisons\n\n")
		b.WriteString("Deltas are clustered bootstraps: tasks are resampled first, then runs within each task, " +
			"so repeated runs of the same task are not counted as independent evidence.\n\n")
		for _, p := range r.Paired {
			fmt.Fprintf(&b, "### %s\n\n", p.Question)
			fmt.Fprintf(&b, "`%s` → `%s`. Isolates: %s.\n\n", p.Baseline, p.Variant, p.WhatItIsolates)
			b.WriteString("| Metric | Delta | 95% interval | Tasks | Runs | Verdict |\n| --- | --- | --- | --- | --- | --- |\n")
			for _, d := range []HierarchicalDelta{p.Success, p.FalseAccept} {
				fmt.Fprintf(&b, "| %s | %+.3f | [%+.3f, %+.3f] | %d | %d | %s |\n",
					d.Metric, d.Delta, d.Low, d.High, d.Tasks, d.Runs, d.Verdict)
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("## Component ladder\n\n")
	b.WriteString("Each rung adds one component to the rung below it. A rung marked *not wired* " +
		"names a component this harness cannot yet switch off, so nothing about its value is claimed here.\n\n")
	b.WriteString("| Rung | Adds | State | Over | Delta | Verdict |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, rung := range r.Ladder {
		state, over, delta, verdict := "not wired", "—", "—", "—"
		if rung.Wired {
			state = "not run"
		}
		if rung.Measured {
			state = "measured"
			over = "`" + rung.Over + "`"
			delta = fmt.Sprintf("%+.3f [%+.3f, %+.3f]", rung.Delta.Delta, rung.Delta.Low, rung.Delta.High)
			verdict = string(rung.Delta.Verdict)
		}
		fmt.Fprintf(&b, "| %s `%s` | %s | %s | %s | %s | %s |\n",
			rung.Letter, rung.Arm, rung.Adds, state, over, delta, verdict)
	}
	b.WriteString("\n")
	for _, rung := range r.Ladder {
		if !rung.Wired {
			fmt.Fprintf(&b, "- **%s** is not wired: %s\n", rung.Adds, rung.Blocker)
		}
	}
	b.WriteString("\n")

	if len(r.Retention) > 0 {
		b.WriteString("## Retention decisions\n\n")
		b.WriteString("A component stays on when the paired evidence supports it, goes behind a flag when " +
			"the evidence is against it, and is left alone when there is not enough evidence either way. " +
			"Nothing here is deleted on the strength of a measurement.\n\n")
		b.WriteString("| Component | Decision | Basis |\n| --- | --- | --- |\n")
		for _, d := range r.Retention {
			fmt.Fprintf(&b, "| %s | `%s` | %s |\n", d.Component, d.Decision, d.Basis)
		}
		b.WriteString("\n")
	}

	b.WriteString("## Qualification gates\n\n")
	b.WriteString("| Gate | Met | Required | Actual |\n| --- | --- | --- | --- |\n")
	for _, g := range r.Qualification.Gates {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", g.Name, mark(g.Met), g.Required, g.Actual)
	}
	b.WriteString("\n")
	for _, g := range r.Qualification.Gates {
		if !g.Met {
			fmt.Fprintf(&b, "- **%s** — %s\n", g.Name, g.Why)
		}
	}
	return b.String()
}

func writeCounts(b *strings.Builder, label string, counts map[string]int) {
	if len(counts) == 0 {
		return
	}
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	fmt.Fprintf(b, "%s: %s\n\n", label, strings.Join(parts, ", "))
}

func formatRate(r Rate) string {
	if r.Total == 0 {
		return "—"
	}
	return fmt.Sprintf("%.0f%% (%d/%d) [%.0f–%.0f%%]",
		r.Value*100, r.Successes, r.Total, r.Low*100, r.High*100)
}

func mark(ok bool) string {
	if ok {
		return "yes"
	}
	return "**no**"
}
