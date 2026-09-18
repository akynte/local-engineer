// Package engine is the adapter contract for whatever actually edits code
// (design v3 DR-5).
//
// The supervisor deliberately owns everything that must be true regardless of
// which engine runs: isolation, the journal, evidence, and the completion
// contract. An engine is handed a bounded objective, a prepared packet and a
// confined worktree, and its output is judged by evidence rather than by what
// it claims. That division is what makes DR-5's replacement path real — a
// different engine changes how edits are produced, not what "done" means.
package engine

import (
	"context"
	"fmt"

	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/workflow"
)

// Request is one step of work.
type Request struct {
	Presets []recipe.Preset
	TaskID  string
	// Objective is the bounded goal for this step, not the whole requirement.
	Objective string
	// Worktree is the writable checkout. It is the only place the engine may
	// change anything, and the sandbox enforces that rather than trusting it.
	Worktree string
	// Packet is the context the supervisor assembled. An engine is not asked
	// to decide what it needs to see; retrieval already did (§8.1).
	Packet *retrieval.Packet
	// Feedback is what verification said about the previous attempt. This is
	// the compiler-and-test loop of §10.1: the single most effective
	// correction signal available.
	Feedback []recipe.Result
	// Attempt counts from 1.
	Attempt int
	// Budget bounds this step.
	Budget Budget
	// Access is supplied by the supervisor, never by the model. An empty
	// write scope permits reads but no native file edits.
	Access firewall.Access
	// Journal records each native tool decision before execution and its
	// outcome afterwards. Nil is reserved for standalone engine evaluations.
	Journal        *ledger.Ledger
	Phase          workflow.Phase
	Transcript     *workflow.Transcript
	SaveTranscript func(context.Context, *workflow.Transcript) error
	Plan           *workflow.Plan
}

// Budget bounds one step so a stuck engine cannot consume a task's whole
// allowance.
type Budget struct {
	ContextTokens   int
	OutputTokens    int
	ReasoningTokens int
	MaxSteps        int
	MaxTokens       int
}

// Response is what an engine reports. None of it is trusted: the supervisor
// re-derives the changed files from the worktree diff, and decides completion
// from evidence.
type Response struct {
	// Summary is the engine's own account of what it did, kept for the
	// journal and for a human reading the history.
	Summary string
	// ClaimsDone is the engine's opinion that the objective is met. It is an
	// input to the decision, never the decision.
	ClaimsDone bool
	// TokensUsed is for telemetry and budget accounting.
	TokensUsed int
	// Edited reports that the engine changed the worktree. The supervisor
	// re-derives the actual change from the diff regardless; this is for
	// telemetry and for spotting an engine that claims completion without
	// having written anything.
	Edited bool
	// Truncated reports that the model hit its output budget before it
	// produced an answer or a tool call.
	//
	// This is kept apart from a model that chose to stop because the two are
	// indistinguishable at the call site — both arrive as a response with no
	// tool calls — and they mean opposite things. One is a result; the other
	// is the engine running out of room, which a reasoning model does by
	// spending the whole budget thinking. Folding them together reports a
	// harness limit as a model verdict, and an evaluation built on that
	// measures the budget rather than the system.
	Truncated bool
	// BudgetExhausted requires a supervisor boundary. The transcript is never
	// trimmed inside an EDIT attempt (architecture review §7).
	BudgetExhausted bool
}

// Engine produces edits in a worktree.
type Engine interface {
	Name() string
	// Step makes progress and returns what it did.
	Step(ctx context.Context, req Request) (*Response, error)
	// Health reports whether the engine can run at all.
	Health(ctx context.Context) error
	Close() error
}

// ErrNoEngine is returned when a task needs edits but no engine is configured.
var ErrNoEngine = fmt.Errorf("engine: no engine configured")

// Edits reports whether an engine is expected to modify the worktree.
//
// The completion contract needs this because passing recipes describe the
// code that is in the worktree, not the work that was done: against an
// untouched worktree every required check passes trivially, so a task that
// edited nothing would be accepted on the strength of the baseline. An engine
// that changes nothing by design says so by implementing `Edits() bool`;
// everything else is assumed to edit, which is the safe default — it can only
// cause a no-op to be refused, never accepted.
func Edits(e Engine) bool {
	if d, ok := e.(interface{ Edits() bool }); ok {
		return d.Edits()
	}
	return true
}

// Verify is the engine used for verification-only tasks: it changes nothing
// and immediately reports that it is done.
//
// It is not a stub. A task that runs the verification recipes against a
// worktree and records the evidence is genuinely useful on its own — it is how
// a change a human made gets the same completion contract as one the system
// made — and it lets the whole task pipeline be exercised, and tested, without
// a model.
type Verify struct{}

func (Verify) Name() string                 { return "verify-only" }
func (Verify) Health(context.Context) error { return nil }
func (Verify) Close() error                 { return nil }

// Edits reports that this engine is not expected to change the worktree, so
// an unchanged worktree is its correct outcome rather than a task that did
// nothing. See Edits.
func (Verify) Edits() bool { return false }

func (Verify) Step(_ context.Context, req Request) (*Response, error) {
	return &Response{
		Summary:    "verification only: the worktree was not modified",
		ClaimsDone: true,
	}, nil
}
