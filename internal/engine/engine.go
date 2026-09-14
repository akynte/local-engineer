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

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
)

// Request is one step of work.
type Request struct {
	TaskID string
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
}

// Budget bounds one step so a stuck engine cannot consume a task's whole
// allowance.
type Budget struct {
	MaxSteps  int
	MaxTokens int
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

func (Verify) Step(_ context.Context, req Request) (*Response, error) {
	return &Response{
		Summary:    "verification only: the worktree was not modified",
		ClaimsDone: true,
	}, nil
}
