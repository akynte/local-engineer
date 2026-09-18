package eval

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Qualification decides whether the evidence is strong enough to stop saying
// the system is unproven.
//
// It exists so that the decision is a computation over what was actually run
// rather than a judgement call by whoever is writing the README that week. Each
// gate is a claim the documentation would otherwise be making implicitly, and
// the gate is what makes it explicit and checkable.
//
// It reports a recommendation. It does not edit documentation: a passing gate
// is evidence that the text may change, not authority to change it.
type Qualification struct {
	ReportSchemaVersion int    `json:"report_schema_version"`
	Status              string `json:"status"` // pass | fail
	Gates               []Gate `json:"gates"`
	// Recommendation is what a maintainer should do next, in words.
	Recommendation string `json:"recommendation"`
}

// Gate is one requirement and whether the data meets it.
type Gate struct {
	Name     string `json:"name"`
	Met      bool   `json:"met"`
	Required string `json:"required"`
	Actual   string `json:"actual"`
	// Why says what the gate is protecting against, so a failing gate tells a
	// reader what would be wrong with publishing without it.
	Why string `json:"why"`
}

// Thresholds are the evidence bar. They are values rather than literals in the
// check so a maintainer can see the whole bar in one place and argue with it.
type Thresholds struct {
	HeldoutRealTasks int `json:"heldout_real_tasks"`
	FinalistRepeats  int `json:"finalist_repeats"`
	MinFinalists     int `json:"min_finalists"`
}

// DefaultThresholds is the bar this project holds itself to.
//
// Thirty held-out real tasks: below that, a single task flipping moves a
// headline rate by more than three points and the interval is wider than any
// effect worth claiming. Three repeats: fewer cannot separate a flaky cell from
// a real one, which the pilot demonstrated by having four of twelve cells
// change verdict between passes.
func DefaultThresholds() Thresholds {
	return Thresholds{HeldoutRealTasks: 30, FinalistRepeats: 3, MinFinalists: 2}
}

// Evidence is everything the gates read. It is assembled from real artefacts;
// nothing here is asserted by hand.
type Evidence struct {
	Tasks    []Task
	Outcomes []Outcome
	Env      Environment
	// Comparisons that were actually computed, by name.
	BaselineComparison bool
	GraphAblation      bool
	ContextAblation    bool
	LadderArms         []string
	// Reproduction is the second clean run's identifier, empty when none.
	EvaluationRunID   string
	ReproductionRunID string
	// ReproductionAgrees is false when the repeat reversed a headline
	// conclusion. A reproduction that disagrees fails qualification rather
	// than being averaged away.
	ReproductionAgrees bool
}

// Qualify runs every gate and reports the result.
func Qualify(e Evidence, t Thresholds) Qualification {
	q := Qualification{ReportSchemaVersion: ReportSchemaVersion}

	heldout := 0
	for _, task := range e.Tasks {
		if task.Membership() == SetHeldout && !task.Synthetic() {
			heldout++
		}
	}
	q.add("heldout_real_tasks", heldout >= t.HeldoutRealTasks,
		fmt.Sprintf(">= %d", t.HeldoutRealTasks), fmt.Sprintf("%d", heldout),
		"Below this a single task flipping moves the headline rate more than any effect worth claiming.")

	minRepeats, finalists := minimumRepeats(e.Outcomes)
	q.add("finalist_repeats", minRepeats >= t.FinalistRepeats && finalists >= t.MinFinalists,
		fmt.Sprintf(">= %d runs on >= %d arms", t.FinalistRepeats, t.MinFinalists),
		fmt.Sprintf("%d runs on %d arms", minRepeats, finalists),
		"One run of a cell is not a measurement of it; the pilot had cells change verdict between passes.")

	hidden := 0
	for _, task := range e.Tasks {
		if len(task.Acceptance.Argv) > 0 {
			hidden++
		}
	}
	q.add("hidden_verification", hidden == len(e.Tasks) && len(e.Tasks) > 0,
		"every task", fmt.Sprintf("%d of %d", hidden, len(e.Tasks)),
		"Without a hidden check the only signal is the model's own claim, which is what this measures against.")

	q.add("baseline_comparison", e.BaselineComparison, "computed", yesNo(e.BaselineComparison),
		"Without an unsupervised baseline there is nothing to attribute an improvement to.")
	// Counting the ladder arms present would pass a set with a hole in it:
	// three arms that are not consecutive measure nothing attributable,
	// because the delta between two rungs two steps apart contains both
	// components and cannot be assigned to either. What counts is the longest
	// unbroken run.
	chain := ladderChain(e.Outcomes)
	q.add("component_ladder", chain >= 3, ">= 3 consecutive rungs",
		fmt.Sprintf("longest unbroken chain: %d", chain),
		"The architecture prescribes measuring each layer's marginal value rather than assuming it, "+
			"and a gap in the ladder makes the deltas across it unattributable.")
	q.add("graph_ablation", e.GraphAblation, "computed", yesNo(e.GraphAblation),
		"The graph's contribution is otherwise a design argument, not a measurement.")
	q.add("context_ablation", e.ContextAblation, "computed", yesNo(e.ContextAblation),
		"Prefix reuse verified against a server is not the same as time saved across a task.")

	far := false
	for _, o := range e.Outcomes {
		if o.Claimed {
			far = true
			break
		}
	}
	q.add("false_acceptance_measured", far, "claims recorded", yesNo(far),
		"A system that fails while reporting success is the failure mode this project exists to prevent.")

	paired := len(e.Outcomes) > 0 && minRepeats > 0
	q.add("paired_statistics", paired, "computed", yesNo(paired),
		"Arms see the same tasks, so unpaired intervals answer a question this design does not ask.")

	q.add("raw_results", len(e.Outcomes) > 0, "present", fmt.Sprintf("%d runs", len(e.Outcomes)),
		"Every aggregate must be derivable from the runs behind it.")

	missing := e.Env.Missing()
	q.add("environment_metadata", len(missing) == 0, "complete",
		strings.Join(append([]string{"missing:"}, missing...), " "),
		"A number without the model, runtime and machine behind it cannot be reproduced or compared.")

	q.add("clean_reproduction", e.ReproductionRunID != "" && e.ReproductionAgrees,
		"a second run agreeing", reproductionState(e),
		"A headline that rests on one execution rests on one roll of the dice.")

	q.Status = "pass"
	var unmet []string
	for _, g := range q.Gates {
		if !g.Met {
			q.Status = "fail"
			unmet = append(unmet, g.Name)
		}
	}
	sort.Strings(unmet)
	if q.Status == "pass" {
		q.Recommendation = "Evidence gates pass. A maintainer may now replace the " +
			"\"not proven to help\" limitation with the generated results, quoting the " +
			"measured numbers and their intervals rather than a summary of them."
	} else {
		q.Recommendation = "Evidence gates fail: " + strings.Join(unmet, ", ") +
			". The \"not proven to help\" limitation stays as written until these are met."
	}
	return q
}

func (q *Qualification) add(name string, met bool, required, actual, why string) {
	q.Gates = append(q.Gates, Gate{Name: name, Met: met, Required: required, Actual: actual, Why: why})
}

// minimumRepeats reports the smallest per-task repeat count across arms, and
// how many arms were run. The minimum is what matters: an arm with three runs
// on nine tasks and one on the tenth is not a three-run arm.
func minimumRepeats(outcomes []Outcome) (int, int) {
	perArmTask := map[string]map[string]int{}
	for _, o := range outcomes {
		if !o.EvidenceRun() {
			continue
		}
		if perArmTask[o.Arm] == nil {
			perArmTask[o.Arm] = map[string]int{}
		}
		perArmTask[o.Arm][o.TaskID]++
	}
	minimum := -1
	for _, tasks := range perArmTask {
		for _, n := range tasks {
			if minimum < 0 || n < minimum {
				minimum = n
			}
		}
	}
	if minimum < 0 {
		minimum = 0
	}
	return minimum, len(perArmTask)
}

func reproductionState(e Evidence) string {
	switch {
	case e.ReproductionRunID == "":
		return "not run"
	case !e.ReproductionAgrees:
		return "run, but a headline conclusion reversed"
	default:
		return "run and agreeing"
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// JSON renders the qualification for CI.
func (q Qualification) JSON() ([]byte, error) { return json.MarshalIndent(q, "", "  ") }
