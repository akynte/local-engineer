package eval

import (
	"regexp"
	"sort"
	"strings"
)

// Grade is how much of a task's hidden acceptance a run satisfied.
//
// Solved stays the headline and stays binary: a task is done or it is not, and
// partial credit is not a product outcome anyone can ship. Grade exists for a
// different job — comparing arms. A binary outcome carries one bit per run, so
// separating two arms that differ slightly needs a number of runs the hardware
// cannot deliver in a working day. The hidden acceptance is already several
// independent assertions; counting how many held turns one bit into several
// and lets the same comparison be made from far fewer runs.
//
// It is a variance-reduction instrument, not a claim that a half-graded task is
// half-useful.
type Grade struct {
	// Total is how many hidden test functions the task defines.
	Total int `json:"total"`
	// Passed is how many of them the run satisfied.
	Passed int `json:"passed"`
	// Failed names the ones that did not, so a score is diagnosable rather
	// than a bare fraction.
	Failed []string `json:"failed,omitempty"`
}

// Score is the fraction satisfied, or zero when the task defines no hidden
// tests to count.
func (g Grade) Score() float64 {
	if g.Total <= 0 {
		return 0
	}
	return float64(g.Passed) / float64(g.Total)
}

// Recorded reports whether this run was graded at all. A result file written
// before grading existed has no grades, and a zero must not be read as "failed
// everything".
func (g Grade) Recorded() bool { return g.Total > 0 }

// testDecl matches a Go test function declaration in a hidden acceptance file.
var testDecl = regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9_]*)\s*\(`)

// failLine matches the line `go test` prints for each failing test.
var failLine = regexp.MustCompile(`(?m)^\s*--- FAIL: (Test[A-Za-z0-9_]*)`)

// hiddenTests lists the test functions a task's acceptance files declare.
//
// The names come from the task rather than from the output because a passing
// test prints nothing: `go test` names failures only. Knowing the whole set in
// advance is what makes "three of four passed" computable from a run that
// printed one FAIL line.
func hiddenTests(task Task) []string {
	seen := map[string]bool{}
	for _, body := range task.Acceptance.Files {
		for _, m := range testDecl.FindAllStringSubmatch(body, -1) {
			seen[m[1]] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// gradeRun scores one run against a task's hidden tests.
//
// A run whose acceptance command exited zero passed all of them, which is the
// only case where the absence of FAIL lines means success. Otherwise the
// failures are counted from the output — and a failing run that names none of
// them did not get as far as running the tests at all, which is a build error
// or a timeout and scores zero rather than full marks.
func gradeRun(task Task, accepted bool, output string) Grade {
	names := hiddenTests(task)
	g := Grade{Total: len(names)}
	if g.Total == 0 {
		return g
	}
	if accepted {
		g.Passed = g.Total
		return g
	}

	known := map[string]bool{}
	for _, n := range names {
		known[n] = true
	}
	failed := map[string]bool{}
	for _, m := range failLine.FindAllStringSubmatch(output, -1) {
		if known[m[1]] {
			failed[m[1]] = true
		}
	}
	if len(failed) == 0 {
		// Nothing ran, or the output no longer says what did. Either way the
		// evidence for a pass is absent, and absent evidence is not a pass.
		return g
	}
	for name := range failed {
		g.Failed = append(g.Failed, name)
	}
	sort.Strings(g.Failed)
	g.Passed = g.Total - len(g.Failed)
	return g
}

// truncatedOutput reports whether the captured output was cut short, in which
// case a FAIL line may have been lost and the grade is a lower bound on what
// passed rather than an exact count.
func truncatedOutput(s string) bool { return strings.Contains(s, "… (output truncated)") }

// GradeWith fills in grades for runs recorded before grading existed, using
// the task set they were run against. It returns how many it graded.
//
// The grade is reconstructed from the acceptance output the run saved, so it is
// exact only while that output is intact. A run whose output was truncated
// could have lost a FAIL line, which would read as a test that passed; those
// are left ungraded rather than graded optimistically, because a score that is
// wrong in the flattering direction is worse than no score.
func (r *Report) GradeWith(tasks []Task) int {
	byID := make(map[string]Task, len(tasks))
	for _, t := range tasks {
		byID[t.ID] = t
	}
	graded := 0
	for i := range r.Outcomes {
		o := &r.Outcomes[i]
		if o.Grade.Recorded() || o.Errored() {
			continue
		}
		task, ok := byID[o.TaskID]
		if !ok || truncatedOutput(o.AcceptanceOutput) {
			continue
		}
		if g := gradeRun(task, o.Solved, o.AcceptanceOutput); g.Recorded() {
			o.Grade = g
			graded++
		}
	}
	if graded > 0 {
		r.derive()
	}
	return graded
}
