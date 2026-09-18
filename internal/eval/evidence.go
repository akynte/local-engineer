package eval

import (
	"encoding/json"
	"os"
	"sort"
)

// LoadOutcomes reads runs from saved report files.
//
// It accepts the shape already written by `le eval run`, so the pilot results
// committed under docs/benchmarks/results/ are readable without converting
// them. A file whose schema version is newer than this build is refused rather
// than half-understood.
func LoadOutcomes(paths ...string) ([]Outcome, error) {
	var all []Outcome
	for _, p := range paths {
		body, err := os.ReadFile(p) //nolint:gosec // operator-supplied result path
		if err != nil {
			return nil, err
		}
		var r struct {
			BenchmarkSchemaVersion int       `json:"benchmark_schema_version"`
			Outcomes               []Outcome `json:"outcomes"`
		}
		if err := json.Unmarshal(body, &r); err != nil {
			return nil, err
		}
		if r.BenchmarkSchemaVersion > BenchmarkSchemaVersion {
			return nil, &SchemaError{Path: p, Found: r.BenchmarkSchemaVersion, Known: BenchmarkSchemaVersion}
		}
		all = append(all, r.Outcomes...)
	}
	return all, nil
}

// SchemaError reports a result file this build cannot interpret.
type SchemaError struct {
	Path  string
	Found int
	Known int
}

func (e *SchemaError) Error() string {
	return "eval: " + e.Path + " uses a newer result schema than this build understands"
}

// ArmsIn lists the arms present in a run set, sorted.
func ArmsIn(outcomes []Outcome) []string {
	seen := map[string]bool{}
	for _, o := range outcomes {
		seen[o.Arm] = true
	}
	out := make([]string, 0, len(seen))
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// has reports whether both arms appear, which is what makes a comparison
// possible at all.
func has(arms []string, a, b string) bool {
	var foundA, foundB bool
	for _, name := range arms {
		foundA = foundA || name == a
		foundB = foundB || name == b
	}
	return foundA && foundB
}

// EvidenceFrom assembles what the gates read from real artefacts.
//
// Every field is derived from the tasks and runs on disk. Nothing is asserted:
// a gate can only pass because the corresponding work was actually done and
// left a trace.
func EvidenceFrom(tasks []Task, outcomes []Outcome, env Environment) Evidence {
	arms := ArmsIn(outcomes)
	e := Evidence{Tasks: tasks, Outcomes: outcomes, Env: env}
	e.BaselineComparison = has(arms, "unsupervised", "supervised")
	e.GraphAblation = has(arms, "supervised-no-graph", "supervised")
	e.ContextAblation = has(arms, "context-dynamic", "context-frozen")
	for _, a := range arms {
		if isLadderArm(a) {
			e.LadderArms = append(e.LadderArms, a)
		}
	}
	return e
}
