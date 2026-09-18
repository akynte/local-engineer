package eval

// The component ladder measures each layer's marginal value instead of
// assuming it.
//
// Every rung differs from the one below it by exactly one toggle, and every
// toggle is a switch that already exists in the system: the arm sets it, the
// solver reads it, and the run records which flags were in force. A rung whose
// component cannot actually be turned off is not a measurement of that
// component, so it is marked unwired and excluded from qualification rather
// than reported as though it had been run.
//
// What is wired today is bounded by where the evaluation harness attaches. It
// drives the native engine, which is where retrieval, the graph and the
// verification loop live. The phased pipeline's later layers — the frozen
// prefix, fresh-context review, obligations and project memory — sit above that
// attachment point, so they are named here with Wired false. Naming them keeps
// the gap visible; omitting them would make the ladder look complete.
type Rung struct {
	// Letter is the ladder position, so a report can say "C over B".
	Letter string `json:"letter"`
	// Arm is the arm name to run.
	Arm string `json:"arm"`
	// Adds is the single component this rung introduces.
	Adds string `json:"adds"`
	// Wired reports whether the toggle actually changes what executes. A rung
	// that is not wired cannot contribute evidence and says so.
	Wired bool `json:"wired"`
	// Blocker explains what would have to be built for an unwired rung.
	Blocker string `json:"blocker,omitempty"`
}

// Ladder returns the rungs in order.
func Ladder() []Rung {
	return []Rung{
		{Letter: "A", Arm: "unsupervised", Adds: "local model and a plain tool loop", Wired: true},
		{Letter: "B", Arm: "supervised-no-graph-no-verify", Adds: "deterministic supervisor: scoped worktree, lexical retrieval, journal", Wired: true},
		{Letter: "C", Arm: "supervised-no-graph", Adds: "verification loop: compiler and tests feed back into the attempt", Wired: true},
		{Letter: "D", Arm: "supervised", Adds: "reference graph: symbol lookup, graph expansion, impact analysis", Wired: true},
		{Letter: "E", Arm: "supervised-review", Adds: "fresh-context review before acceptance", Wired: false,
			Blocker: "the harness drives the native engine; review lives in the phased runner above it"},
		{Letter: "F", Arm: "context-frozen", Adds: "frozen-prefix context architecture", Wired: false,
			Blocker: "the context packer is in the phased runner; the harness would have to attach there"},
		{Letter: "G", Arm: "supervised-memory", Adds: "project memory carried between tasks", Wired: false,
			Blocker: "memory is per-repository and the harness gives each task a fresh fixture"},
		{Letter: "H", Arm: "supervised-embeddings", Adds: "semantic embedding retrieval", Wired: false,
			Blocker: "optional component, not implemented; §6.5 requires measuring before adding"},
		{Letter: "I", Arm: "supervised-reranker", Adds: "reranking over embedding candidates", Wired: false,
			Blocker: "depends on H"},
	}
}

// WiredLadder returns only the rungs that can be run today.
func WiredLadder() []Rung {
	var out []Rung
	for _, r := range Ladder() {
		if r.Wired {
			out = append(out, r)
		}
	}
	return out
}

// isLadderArm reports whether an arm name is a wired ladder rung. Only wired
// rungs count toward the component-ladder gate: an arm that does not change
// what executes has not measured anything.
func isLadderArm(name string) bool {
	for _, r := range WiredLadder() {
		if r.Arm == name {
			return true
		}
	}
	return false
}

// Retention is the evidence-based rule for whether a component stays on by
// default.
//
// The policy exists so that "we removed it because it did not seem to help" is
// not a thing anyone can say. A component is kept on evidence, disabled on
// evidence, and left alone when the evidence is absent — which is a third
// outcome, distinct from the other two.
type Retention struct {
	Component string  `json:"component"`
	Decision  string  `json:"decision"` // keep_enabled | disable_by_default | insufficient_evidence
	Basis     string  `json:"basis"`
	Delta     float64 `json:"delta"`
	Verdict   Verdict `json:"verdict"`
}

// RetentionThresholds is the bar a component must clear to stay on by default.
type RetentionThresholds struct {
	// SuccessDelta is the paired improvement in task success that justifies a
	// component on quality grounds.
	SuccessDelta float64
	// WallClockFraction is the share of wall-clock time a component must save
	// to justify itself on cost grounds when quality is unchanged.
	WallClockFraction float64
}

// DefaultRetention is the bar this project applies.
func DefaultRetention() RetentionThresholds {
	return RetentionThresholds{SuccessDelta: 0.0, WallClockFraction: 0.20}
}

// Decide applies the retention policy to one measured component.
//
// A component stays enabled when the paired evidence is positive on any of the
// metrics that matter. It is disabled by default — not deleted — when the
// evidence is negative, because a component that does not pay on this task set
// may pay on another, and deleting it destroys the option. Insufficient
// evidence changes nothing: the burden is on the measurement, not on the code.
func Decide(component string, success HierarchicalDelta, t RetentionThresholds) Retention {
	r := Retention{Component: component, Delta: success.Delta, Verdict: success.Verdict}
	switch success.Verdict {
	case VerdictPositive:
		r.Decision, r.Basis = "keep_enabled", "paired task-success improvement excludes zero"
	case VerdictNegative:
		r.Decision, r.Basis = "disable_by_default", "paired task-success difference is negative; kept behind a flag, not deleted"
	case VerdictNoMaterialDifference:
		r.Decision, r.Basis = "insufficient_evidence", "no material difference in task success; a cost-side case must be made separately"
	default:
		r.Decision, r.Basis = "insufficient_evidence", "the interval is too wide to distinguish an effect from its absence"
	}
	return r
}

// ladderChain returns the length of the longest run of consecutive wired rungs
// for which evidence runs exist.
//
// Consecutiveness is the whole property. Rungs A, C and D with B missing give
// three arms and one attributable delta: the gap from A to C carries two
// components at once, and no statistic can separate them afterwards.
func ladderChain(outcomes []Outcome) int {
	present := map[string]bool{}
	for _, o := range outcomes {
		if o.EvidenceRun() {
			present[o.Arm] = true
		}
	}
	best, run := 0, 0
	for _, r := range WiredLadder() {
		if present[r.Arm] {
			run++
			if run > best {
				best = run
			}
			continue
		}
		run = 0
	}
	return best
}
