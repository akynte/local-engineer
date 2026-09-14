// Package broker implements the human gates of design v3 §3.3: the points
// where a decision leaves the system and waits for a person.
//
// The design exposes impact analysis "to the planner before edits, to the plan
// reviewer, and to the human gate". This package is that third place. Its job
// is to make the decision cheap to take: a gate carries the deterministic
// evidence for the decision, not a model's description of it, so approving is
// reading a diff and an impact report rather than trusting a summary.
//
// A gate is recorded in the ledger before it blocks, so an interrupted
// approval is a pending gate on restart rather than a lost one.
package broker

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Kind is what a gate is asking about.
type Kind string

const (
	// KindPlan asks whether a decomposition should be executed.
	KindPlan Kind = "plan"
	// KindImpact asks whether a change with breaking consumers should proceed.
	KindImpact Kind = "impact"
	// KindOutOfScope asks whether a change outside the declared scope is
	// acceptable.
	KindOutOfScope Kind = "out_of_scope"
	// KindBudget asks whether an exhausted budget should be extended.
	KindBudget Kind = "budget"
	// KindApply asks whether a completed task's diff should be applied.
	KindApply Kind = "apply"
)

// Decision is a gate's outcome.
type Decision string

const (
	Pending  Decision = "pending"
	Approved Decision = "approved"
	Rejected Decision = "rejected"
	// Expired: nobody answered within the gate's deadline. It is distinct
	// from rejection, because "nobody looked" and "a person said no" call for
	// different responses.
	Expired Decision = "expired"
)

// Gate is one decision awaiting a person.
type Gate struct {
	ID          string       `json:"id"`
	WorkspaceID workspace.ID `json:"workspace_id"`
	TaskID      string       `json:"task_id"`
	Kind        Kind         `json:"kind"`
	Question    string       `json:"question"`
	// Evidence is what the decision rests on: an impact report, a diff, the
	// verification findings. It is assembled by the supervisor, so the person
	// deciding reads the deterministic answer rather than a model's account.
	Evidence json.RawMessage `json:"evidence,omitempty"`
	Decision Decision        `json:"decision"`
	// DecidedBy and Note record who answered and why, because a gate's value
	// six months later is the reasoning, not the verdict.
	DecidedBy string     `json:"decided_by,omitempty"`
	Note      string     `json:"note,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	DecidedAt *time.Time `json:"decided_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

// Open reports whether the gate is still waiting.
func (g Gate) Open() bool { return g.Decision == Pending }

// Policy decides which gates need a person and which are automatic.
//
// The default is deliberately not "approve everything": a system that never
// asks is one whose gates are decoration. It is also not "ask about
// everything", which trains people to approve without reading.
type Policy struct {
	// RequireForBreaking gates a change whose impact report names breaking
	// consumers.
	RequireForBreaking bool
	// RequireForOutOfScope gates any change outside the declared scope.
	RequireForOutOfScope bool
	// RequireForApply gates applying a completed task's diff.
	RequireForApply bool
	// RequireForPlan gates a decomposition before it runs.
	RequireForPlan bool
	// MaxBreakingAuto is how many breaking consumers may pass without a
	// person, when RequireForBreaking is off. Zero means none.
	MaxBreakingAuto int
	// Timeout is how long a gate waits. Zero means indefinitely.
	Timeout time.Duration
}

// DefaultPolicy gates the decisions whose cost of being wrong is high and
// whose evidence a person can check quickly.
func DefaultPolicy() Policy {
	return Policy{
		RequireForBreaking:   true,
		RequireForOutOfScope: true,
		RequireForApply:      true,
		RequireForPlan:       false,
		Timeout:              0,
	}
}

// Permissive approves everything automatically. It exists for unattended runs
// and for tests, and `le doctor` reports when it is in force: a system running
// without gates should never be a surprise.
func Permissive() Policy { return Policy{} }

// Needs reports whether a decision of this kind requires a person under the
// policy, and why.
func (p Policy) Needs(kind Kind, ev Evidence) (bool, string) {
	switch kind {
	case KindPlan:
		if p.RequireForPlan {
			return true, "the policy gates plans before they run"
		}
	case KindImpact:
		breaking := ev.BreakingCount
		if p.RequireForBreaking && breaking > 0 {
			return true, fmt.Sprintf("the change breaks %d consumer(s)", breaking)
		}
		if !p.RequireForBreaking && breaking > p.MaxBreakingAuto {
			return true, fmt.Sprintf("%d breaking consumer(s) exceeds the automatic limit of %d",
				breaking, p.MaxBreakingAuto)
		}
	case KindOutOfScope:
		if p.RequireForOutOfScope && len(ev.OutOfScope) > 0 {
			return true, fmt.Sprintf("%d file(s) changed outside the declared scope", len(ev.OutOfScope))
		}
	case KindApply:
		if p.RequireForApply {
			return true, "the policy gates applying a change"
		}
	case KindBudget:
		// A budget increase is always a person's call: the budget exists
		// precisely so that a task cannot decide to keep going.
		return true, "extending a budget is not a decision the system makes for itself"
	}
	return false, ""
}

// Evidence is what a gate carries.
type Evidence struct {
	Summary       string        `json:"summary"`
	Impact        *graph.Impact `json:"impact,omitempty"`
	BreakingCount int           `json:"breaking_count,omitempty"`
	OutOfScope    []string      `json:"out_of_scope,omitempty"`
	Diff          string        `json:"diff,omitempty"`
	Findings      []string      `json:"findings,omitempty"`
	Plan          any           `json:"plan,omitempty"`
}

// MaxDiffInGate bounds the diff a gate carries. A person asked to approve a
// 50,000-line diff is not reviewing it, and pretending otherwise is worse than
// pointing at the worktree.
const MaxDiffInGate = 40000

// Broker records and resolves gates.
type Broker struct {
	db     *store.DB
	ws     workspace.ID
	ledger *ledger.Ledger
	policy Policy
}

// New binds a broker to a workspace.
func New(s *store.Store, policy Policy) *Broker {
	return &Broker{db: s.Ledger(), ws: s.ID(), ledger: ledger.New(s), policy: policy}
}

// Policy reports the active policy, for `le doctor`.
func (b *Broker) Policy() Policy { return b.policy }

// ErrRejected is returned when a person declined.
var ErrRejected = errors.New("broker: rejected at the human gate")

// ErrPending is returned when a gate was opened and nobody has answered.
var ErrPending = errors.New("broker: waiting at a human gate")

// Ask opens a gate if the policy requires one, and returns the decision.
//
// When the policy does not require a gate, the decision is Approved and
// nothing is recorded: a ledger full of automatic approvals is noise that
// makes the real gates harder to find.
func (b *Broker) Ask(ctx context.Context, taskID string, kind Kind, question string, ev Evidence) (Gate, error) {
	needed, why := b.policy.Needs(kind, ev)
	if !needed {
		return Gate{TaskID: taskID, Kind: kind, Decision: Approved,
			Note: "no gate required by policy"}, nil
	}
	if len(ev.Diff) > MaxDiffInGate {
		ev.Diff = ev.Diff[:MaxDiffInGate] +
			"\n… diff truncated; inspect the task worktree for the whole change\n"
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return Gate{}, err
	}

	g := Gate{
		ID:          fmt.Sprintf("gate-%s-%s-%d", taskID, kind, time.Now().UnixMilli()),
		WorkspaceID: b.ws, TaskID: taskID, Kind: kind,
		Question: strings.TrimSpace(question + " (" + why + ")"),
		Evidence: body, Decision: Pending, CreatedAt: time.Now().UTC(),
	}
	if b.policy.Timeout > 0 {
		exp := g.CreatedAt.Add(b.policy.Timeout)
		g.ExpiresAt = &exp
	}

	// The gate is journalled before it blocks, so an interrupted approval is a
	// pending gate on restart rather than a lost one.
	h, err := b.ledger.Begin(ctx, taskID, ledger.KindApproval, map[string]any{
		"gate": g.ID, "kind": string(kind), "question": g.Question, "reason": why,
	}, "")
	if err != nil {
		return Gate{}, err
	}
	if err := b.save(ctx, g); err != nil {
		return Gate{}, errors.Join(err, h.Fail(ctx, err))
	}
	// The operation stays open until the gate is decided: an approval nobody
	// answered is exactly the uncertainty the journal is designed to record.
	return g, nil
}

// Decide resolves a gate.
func (b *Broker) Decide(ctx context.Context, gateID string, decision Decision, by, note string) (Gate, error) {
	g, err := b.Get(ctx, gateID)
	if err != nil {
		return g, err
	}
	if !g.Open() {
		return g, fmt.Errorf("broker: gate %s was already %s", gateID, g.Decision)
	}
	now := time.Now().UTC()
	g.Decision, g.DecidedBy, g.Note, g.DecidedAt = decision, by, note, &now

	if err := b.save(ctx, g); err != nil {
		return g, err
	}
	// Close the journalled approval with the answer.
	ops, err := b.ledger.Operations(ctx, g.TaskID)
	if err != nil {
		return g, err
	}
	for _, op := range ops {
		if op.Kind != ledger.KindApproval || !op.Uncertain() {
			continue
		}
		var intent struct {
			Gate string `json:"gate"`
		}
		if json.Unmarshal(op.Intent, &intent) == nil && intent.Gate == gateID {
			if err := b.complete(ctx, op.ID, decision, by, note); err != nil {
				return g, err
			}
			break
		}
	}
	return g, nil
}

func (b *Broker) complete(ctx context.Context, opID int64, decision Decision, by, note string) error {
	outcome, err := json.Marshal(map[string]string{
		"decision": string(decision), "by": by, "note": note,
	})
	if err != nil {
		return err
	}
	return b.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE operations SET outcome = ?, finished_at = ? WHERE id = ?`,
			string(outcome), time.Now().UnixMilli(), opID)
		return err
	})
}

// Pending lists the gates still waiting, expiring any that are past their
// deadline. Expiry happens on read rather than on a timer: a gate nobody has
// looked at has not expired in any meaningful sense until someone looks.
func (b *Broker) Pending(ctx context.Context) ([]Gate, error) {
	gates, err := b.list(ctx, "WHERE decision = 'pending'")
	if err != nil {
		return nil, err
	}
	now := time.Now()
	out := make([]Gate, 0, len(gates))
	for _, g := range gates {
		if g.ExpiresAt != nil && now.After(*g.ExpiresAt) {
			if decided, err := b.Decide(ctx, g.ID, Expired, "system",
				"nobody answered within the policy timeout"); err == nil {
				out = append(out, decided)
				continue
			}
		}
		out = append(out, g)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

// All lists every gate for a workspace.
func (b *Broker) All(ctx context.Context) ([]Gate, error) { return b.list(ctx, "") }

// Get loads one gate.
func (b *Broker) Get(ctx context.Context, id string) (Gate, error) {
	gates, err := b.list(ctx, "WHERE id = ?", id)
	if err != nil {
		return Gate{}, err
	}
	if len(gates) == 0 {
		return Gate{}, fmt.Errorf("broker: no gate %q", id)
	}
	return gates[0], nil
}
