// Package workflow defines the persisted architecture-review phase contract.
package workflow

import (
	"fmt"
	"path"
	"strings"

	"github.com/akynte/local-engineer/internal/contextpack"
	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/policy"
	"github.com/akynte/local-engineer/internal/recipe"
)

type Phase string

const (
	Intake   Phase = "INTAKE"
	Localize Phase = "LOCALIZE"
	Impact   Phase = "IMPACT"
	Planning Phase = "PLAN"
	Edit     Phase = "EDIT"
	Verify   Phase = "VERIFY"
	Review   Phase = "REVIEW"
	Finalize Phase = "FINALIZE"
)

func Transition(from, to Phase) error {
	allowed := map[Phase][]Phase{
		Intake: {Localize}, Localize: {Impact}, Impact: {Planning}, Planning: {Edit},
		Edit: {Verify, Planning, Localize}, Verify: {Edit, Review, Localize, Planning},
		Review: {Edit, Localize, Finalize}, Finalize: {},
	}
	for _, next := range allowed[from] {
		if next == to {
			return nil
		}
	}
	return fmt.Errorf("workflow: forbidden transition %s -> %s", from, to)
}

type Obligation struct {
	Symbol     string `json:"symbol"`
	Path       string `json:"path"`
	Reason     string `json:"reason"`
	Resolution string `json:"resolution,omitempty"`
}

type Plan struct {
	RootCause      string       `json:"root_cause"`
	Files          []string     `json:"files"`
	Symbols        []string     `json:"symbols"`
	Tests          []string     `json:"tests"`
	Contracts      []string     `json:"contracts"`
	WriteAllowlist []string     `json:"write_allowlist"`
	Risks          []string     `json:"risks"`
	Obligations    []Obligation `json:"obligations"`
	// Regenerate names frozen presets of kind "generate" that this change
	// makes stale. Declaring one is how a plan changes generated code: the
	// supervisor runs the generator, so what lands is what the generator
	// produces rather than what a model believed it would produce.
	Regenerate []string `json:"regenerate"`
}

// RegeneratesGenerated reports whether the plan declared any generator, which
// is what makes a changed generated file an expected outcome rather than an
// edit outside the allowlist.
func (p Plan) RegeneratesGenerated() bool { return len(p.Regenerate) > 0 }

// ValidateRegeneration resolves each declared generator against the presets
// frozen during INTAKE. A model cannot name a command; it can only select one
// the operator already froze, and only one whose kind is generation.
func (p Plan) ValidateRegeneration(presets []recipe.Preset) error {
	for _, name := range p.Regenerate {
		var found *recipe.Preset
		for i := range presets {
			if presets[i].Name == name {
				found = &presets[i]
				break
			}
		}
		if found == nil {
			return fmt.Errorf("plan declares regeneration by %q, which is not a frozen verification preset", name)
		}
		if found.Kind != recipe.KindGenerate {
			return fmt.Errorf("plan declares regeneration by %q, whose kind is %q rather than generate", name, found.Kind)
		}
	}
	return nil
}

func (p Plan) Validate(operatorScope []string) error {
	if strings.TrimSpace(p.RootCause) == "" || len(p.Files) == 0 || len(p.WriteAllowlist) == 0 || len(p.Tests) == 0 {
		return fmt.Errorf("plan requires a hypothesis, files, write allowlist and tests")
	}
	// Exact file grants avoid an untrusted planner broadening a glob beyond
	// operator authority. New files must be declared explicitly too.
	for _, f := range append(append([]string{}, p.Files...), p.WriteAllowlist...) {
		if f == "." || path.IsAbs(f) || path.Clean(f) != f || strings.ContainsAny(f, "*?[\\") ||
			f == ".." || strings.HasPrefix(f, "../") || policy.Sensitive(f) || strings.HasPrefix(f, ".git/") {
			return fmt.Errorf("plan contains invalid file grant %q", f)
		}
		// Granting a generated file would let the model hand-edit output its
		// generator owns. The plan says which generator to run instead.
		if firewall.Generated(f) {
			return fmt.Errorf("plan grants writes to generated file %q; declare its generator under regenerate instead", f)
		}
		if len(operatorScope) > 0 && !policy.Covers(operatorScope, f) {
			return fmt.Errorf("plan file %q exceeds operator scope", f)
		}
	}
	for _, f := range p.WriteAllowlist {
		found := false
		for _, declared := range p.Files {
			found = found || declared == f
		}
		if !found {
			return fmt.Errorf("write grant %q is not a declared file", f)
		}
	}
	return nil
}

// Transcript is append-only during an EDIT phase. Pending records the crash
// window around a tool side effect; it must be reconciled, never replayed.
type Transcript struct {
	Closed          bool          `json:"closed"`
	Summary         string        `json:"summary"`
	ClaimsDone      bool          `json:"claims_done"`
	BudgetExhausted bool          `json:"budget_exhausted"`
	Truncated       bool          `json:"truncated"`
	FenceToken      string        `json:"fence_token"`
	Messages        []llm.Message `json:"messages"`
	Tokens          int           `json:"tokens"`
	Steps           int           `json:"steps"`
	Tools           int           `json:"tools"`
	VerifyRuns      int           `json:"verify_runs"`
	InvalidCalls    int           `json:"invalid_calls"`
	Pending         bool          `json:"pending"`
	Candidate       string        `json:"candidate"`
}

type Verdict struct {
	Accept   bool     `json:"accept"`
	Findings []string `json:"findings"`
}

type State struct {
	// Prefix is §7.1's frozen region, built once and persisted. It is stored
	// rather than recomputed because a repository map rebuilt per call would
	// rank identically only by luck, and one reordered row invalidates every
	// cached token after it.
	Prefix contextpack.Prefix `json:"prefix"`
	// FenceToken is the task's fence. One per task, not one per call: the
	// token is part of the policy preamble, so a fresh one each time changed
	// the first message of every request.
	FenceToken            string            `json:"fence_token,omitempty"`
	Bodies                map[string]string `json:"localized_bodies,omitempty"`
	FinalizationCandidate string            `json:"finalization_candidate,omitempty"`
	Presets               []recipe.Preset   `json:"presets"`
	Phase                 Phase             `json:"phase"`
	Base                  string            `json:"base"`
	Candidate             string            `json:"candidate"`
	StartedAt             int64             `json:"started_at"`
	Tokens                int               `json:"tokens"`
	Attempts              int               `json:"attempts"`
	Replans               int               `json:"replans"`
	VerifyLoops           int               `json:"verify_loops"`
	Regressions           int               `json:"regressions"`
	Files                 []string          `json:"files"`
	Symbols               []string          `json:"symbols"`
	Hypothesis            string            `json:"hypothesis"`
	Plan                  Plan              `json:"plan"`
	Impact                *graph.Impact     `json:"impact,omitempty"`
	Baseline              []recipe.Result   `json:"baseline"`
	Results               []recipe.Result   `json:"results"`
	Feedback              []recipe.Result   `json:"feedback"`
	Failures              map[string]int    `json:"failures"`
	FailureHistory        []FailureRecord   `json:"failure_history"`
	Edit                  Transcript        `json:"edit"`
	Verdict               *Verdict          `json:"verdict,omitempty"`
}
