// Package ledger implements the execution journal and recovery procedure of
// design v3 §7.
//
// The contract is intent-first: every model-visible action writes its intent
// BEFORE the side effect and its outcome AFTER. An operation with a NULL
// outcome is *uncertain*, and uncertainty is what recovery reconciles — it is
// never treated as either success or failure.
package ledger

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Kind enumerates the operation kinds listed in the §7.1 schema comment.
type Kind string

const (
	KindInspectFile  Kind = "inspect_file"
	KindSearch       Kind = "search"
	KindRetrieval    Kind = "retrieval"
	KindDecision     Kind = "decision"
	KindEdit         Kind = "edit"
	KindRecipeRun    Kind = "recipe_run"
	KindReview       Kind = "review"
	KindCheckpoint   Kind = "checkpoint"
	KindApproval     Kind = "approval"
	KindSessionStart Kind = "session_start"
	KindSessionEnd   Kind = "session_end"
)

// AllKinds is used for CLI validation and for the dashboard's filter list.
func AllKinds() []Kind {
	return []Kind{KindInspectFile, KindSearch, KindRetrieval, KindDecision, KindEdit,
		KindRecipeRun, KindReview, KindCheckpoint, KindApproval, KindSessionStart, KindSessionEnd}
}

// Ledger is the journal for one workspace.
type Ledger struct {
	db *store.DB
	ws workspace.ID
}

// New binds a ledger to a workspace store.
func New(s *store.Store) *Ledger { return &Ledger{db: s.Ledger(), ws: s.ID()} }

// WorkspaceID reports the workspace this ledger records.
func (l *Ledger) WorkspaceID() workspace.ID { return l.ws }

// Operation is one journal row.
type Operation struct {
	ID              int64           `json:"id"`
	TaskID          string          `json:"task_id"`
	Seq             int64           `json:"seq"`
	Kind            Kind            `json:"kind"`
	Intent          json.RawMessage `json:"intent"`
	Outcome         json.RawMessage `json:"outcome,omitempty"`
	CandidateBefore string          `json:"candidate_before,omitempty"`
	CandidateAfter  string          `json:"candidate_after,omitempty"`
	EvidenceID      string          `json:"evidence_id,omitempty"`
	StartedAt       int64           `json:"started_at"`
	FinishedAt      int64           `json:"finished_at,omitempty"`
	// Error is why an interrupted operation stopped, when the cause was
	// caught. It is deliberately separate from Outcome: an operation with a
	// cause and no outcome is still uncertain, because the side effect may
	// have taken hold before the failure.
	Error string `json:"error,omitempty"`
}

// Uncertain reports whether the outcome was never written, which is the state
// §7.2 step 1 must classify by inspection.
func (o Operation) Uncertain() bool { return len(o.Outcome) == 0 }

// Begin writes the intent row and returns its handle. The side effect must not
// start until this returns: that ordering is the whole point of the journal.
func (l *Ledger) Begin(ctx context.Context, taskID string, kind Kind, intent any, candidateBefore string) (*Handle, error) {
	body, err := json.Marshal(intent)
	if err != nil {
		return nil, fmt.Errorf("ledger: marshal intent: %w", err)
	}
	h := &Handle{l: l, taskID: taskID, kind: kind}
	err = l.db.Tx(ctx, func(tx *sql.Tx) error {
		var seq int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM operations WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO operations (task_id, seq, kind, intent, candidate_before, started_at)
			VALUES (?,?,?,?,?,?)`,
			taskID, seq, string(kind), string(body), candidateBefore, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		id, err := res.LastInsertId()
		if err != nil {
			return err
		}
		h.id, h.seq = id, seq
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("ledger: begin %s for %s: %w", kind, taskID, err)
	}
	return h, nil
}

// Handle is an in-flight operation. Exactly one of Complete or Fail must be
// called; leaving it open is what marks the operation uncertain after a crash.
type Handle struct {
	l      *Ledger
	id     int64
	seq    int64
	taskID string
	kind   Kind
	done   bool
}

// ID and Seq identify the operation.
func (h *Handle) ID() int64  { return h.id }
func (h *Handle) Seq() int64 { return h.seq }

// Complete writes the outcome after the side effect has happened.
func (h *Handle) Complete(ctx context.Context, outcome any, candidateAfter, evidenceID string) error {
	return h.finish(ctx, outcome, candidateAfter, evidenceID)
}

// Fail records a definite failure. A recorded failure is *not* uncertain: the
// side effect is known not to have taken hold, so recovery does not re-inspect.
//
// Only call this when that is actually true — when the operation failed before
// it could touch anything. An operation that failed partway through has an
// uncertain side effect, and saying otherwise hides it from recovery. Use
// Interrupted for that case.
func (h *Handle) Fail(ctx context.Context, cause error) error {
	return h.finish(ctx, map[string]string{"status": "failed", "error": cause.Error()}, "", "")
}

// Interrupted records why an operation stopped without claiming its side effect
// did not happen.
//
// This is the honest outcome for anything that can fail partway through. An
// engine step, for example, reads files, edits them and runs verification
// before it returns; an error from the middle of that leaves a worktree nobody
// has looked at. Recording it as a definite failure would mark the operation
// certain, and recovery skips inspection for certain operations — so a
// half-applied set of edits would never be examined.
//
// The operation therefore stays uncertain (outcome NULL) and the cause is kept
// beside it, which is the state §7.2 is built to reconcile.
func (h *Handle) Interrupted(ctx context.Context, cause error) error {
	if h.done {
		return fmt.Errorf("ledger: operation %d already finished", h.id)
	}
	if cause == nil {
		cause = errors.New("interrupted")
	}
	err := h.l.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE operations SET error = ?, finished_at = ? WHERE id = ?`,
			cause.Error(), time.Now().UnixMilli(), h.id)
		return err
	})
	if err != nil {
		return fmt.Errorf("ledger: record interruption for %s (%d): %w", h.kind, h.id, err)
	}
	// The handle is spent: an interrupted operation must not later be completed
	// as though nothing happened.
	h.done = true
	return nil
}

func (h *Handle) finish(ctx context.Context, outcome any, candidateAfter, evidenceID string) error {
	if h.done {
		return fmt.Errorf("ledger: operation %d already finished", h.id)
	}
	body, err := json.Marshal(outcome)
	if err != nil {
		return fmt.Errorf("ledger: marshal outcome: %w", err)
	}
	err = h.l.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE operations SET outcome = ?, candidate_after = ?, evidence_id = ?, finished_at = ?
			WHERE id = ?`,
			string(body), nullable(candidateAfter), nullable(evidenceID), time.Now().UnixMilli(), h.id)
		return err
	})
	if err != nil {
		return fmt.Errorf("ledger: complete %s (%d): %w", h.kind, h.id, err)
	}
	h.done = true
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Operations returns a task's journal in sequence order.
func (l *Ledger) Operations(ctx context.Context, taskID string) ([]Operation, error) {
	rows, err := l.db.SQL().QueryContext(ctx, `
		SELECT id, task_id, seq, kind, intent, COALESCE(outcome,''), COALESCE(candidate_before,''),
		       COALESCE(candidate_after,''), COALESCE(evidence_id,''), COALESCE(started_at,0),
		       COALESCE(finished_at,0), COALESCE(error,'')
		FROM operations WHERE task_id = ? ORDER BY seq`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Operation
	for rows.Next() {
		var o Operation
		var intent, outcome string
		if err := rows.Scan(&o.ID, &o.TaskID, &o.Seq, &o.Kind, &intent, &outcome,
			&o.CandidateBefore, &o.CandidateAfter, &o.EvidenceID, &o.StartedAt, &o.FinishedAt,
			&o.Error); err != nil {
			return nil, err
		}
		o.Intent = json.RawMessage(intent)
		if outcome != "" {
			o.Outcome = json.RawMessage(outcome)
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

// Checkpoint records the working state a resume can start from (§7.1).
func (l *Ledger) Checkpoint(ctx context.Context, taskID string, state string, handoff any, candidate string) error {
	body, err := json.Marshal(handoff)
	if err != nil {
		return err
	}
	return l.db.Tx(ctx, func(tx *sql.Tx) error {
		var seq int64
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM operations WHERE task_id = ?`, taskID).Scan(&seq); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `
			INSERT INTO checkpoints (task_id, seq, state, handoff, candidate, created_at)
			VALUES (?,?,?,?,?,?)
			ON CONFLICT (task_id, seq) DO UPDATE SET
			  state = excluded.state, handoff = excluded.handoff,
			  candidate = excluded.candidate, created_at = excluded.created_at`,
			taskID, seq, state, string(body), nullable(candidate), time.Now().UnixMilli())
		return err
	})
}

// LastCheckpoint returns the most recent checkpoint for a task.
func (l *Ledger) LastCheckpoint(ctx context.Context, taskID string) (seq int64, state string, handoff json.RawMessage, candidate string, err error) {
	var body string
	row := l.db.SQL().QueryRowContext(ctx, `
		SELECT seq, state, handoff, COALESCE(candidate,'') FROM checkpoints
		WHERE task_id = ? ORDER BY seq DESC LIMIT 1`, taskID)
	err = row.Scan(&seq, &state, &body, &candidate)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil, "", nil
	}
	return seq, state, json.RawMessage(body), candidate, err
}
