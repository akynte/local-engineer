// Package task implements the task lifecycle and the completion contract.
//
// The contract is the point of the whole design: a task is accepted only when
// evidence says so. Specifically, acceptance requires that every recipe kind
// the task's verification level demands has a passing result, produced against
// the worktree's *current* candidate, with no out-of-scope writes. An engine's
// claim that it is finished is an input to that decision and never the
// decision itself (design v3 §10.1, §7).
package task

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// State is a task's lifecycle state. These match the ledger's CHECK constraint.
type State string

const (
	StatePending   State = "pending"
	StateRunning   State = "running"
	StatePaused    State = "paused"
	StateBlocked   State = "blocked"
	StateReview    State = "review"
	StateAccepted  State = "accepted"
	StateFailed    State = "failed"
	StateAbandoned State = "abandoned"
)

// Terminal reports whether no further work is expected.
func (s State) Terminal() bool {
	return s == StateAccepted || s == StateFailed || s == StateAbandoned
}

// Task is one unit of work.
type Task struct {
	ID            string       `json:"id"`
	WorkspaceID   workspace.ID `json:"workspace_id"`
	RequirementID string       `json:"requirement_id,omitempty"`
	ParentID      string       `json:"parent_id,omitempty"`
	Title         string       `json:"title"`
	Kind          string       `json:"kind"`
	WorktreeID    string       `json:"worktree_id,omitempty"`
	State         State        `json:"state"`
	Verification  recipe.Level `json:"verification"`
	Budget        Budget       `json:"budget"`
	CreatedAt     time.Time    `json:"created_at"`
	UpdatedAt     time.Time    `json:"updated_at"`
	FinishedAt    *time.Time   `json:"finished_at,omitempty"`
}

// Budget bounds a task. A task that cannot finish inside its budget is blocked
// for a human rather than allowed to run indefinitely.
type Budget struct {
	MaxAttempts int           `json:"max_attempts"`
	MaxWallTime time.Duration `json:"max_wall_time"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	// Scope authorizes native editor writes before execution. Empty grants
	// no native writes. The final diff is checked independently for all engines.
	Scope []string `json:"scope,omitempty"`
}

// DefaultBudget is used when a task declares none.
func DefaultBudget() Budget {
	return Budget{MaxAttempts: 3, MaxWallTime: 30 * time.Minute}
}

// Requirement is what a task serves, with its executable acceptance criteria.
type Requirement struct {
	ID         string    `json:"id"`
	Title      string    `json:"title"`
	Body       string    `json:"body,omitempty"`
	Acceptance []Check   `json:"acceptance"`
	State      string    `json:"state"`
	CreatedAt  time.Time `json:"created_at"`
}

// Check is one executable acceptance criterion.
//
// "Executable" is the operative word: a criterion the system cannot run is a
// wish, not a criterion. A requirement whose acceptance is prose can still be
// tracked, but it can never be automatically accepted, and Store makes that
// explicit rather than letting it pass quietly.
type Check struct {
	Name string `json:"name"`
	// Argv is the command that decides the criterion. Empty means the
	// criterion is manual and requires a human gate.
	Argv []string `json:"argv,omitempty"`
	// Description is what the criterion means, for a human reading the gate.
	Description string `json:"description,omitempty"`
}

// Executable reports whether the check can be decided without a human.
func (c Check) Executable() bool { return len(c.Argv) > 0 }

// Store persists tasks and requirements in a workspace's ledger.
type Store struct {
	db *store.DB
	ws workspace.ID
}

// NewStore binds a task store to a workspace.
func NewStore(s *store.Store) *Store { return &Store{db: s.Ledger(), ws: s.ID()} }

// CreateRequirement records a requirement.
func (s *Store) CreateRequirement(ctx context.Context, r Requirement) error {
	if r.ID == "" {
		return errors.New("task: requirement needs an id")
	}
	body, err := json.Marshal(r.Acceptance)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO requirements (id, workspace_id, title, body, acceptance, state, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?)
			ON CONFLICT (id) DO UPDATE SET title = excluded.title, body = excluded.body,
			  acceptance = excluded.acceptance, updated_at = excluded.updated_at`,
			r.ID, s.ws.String(), r.Title, r.Body, string(body), orDefault(r.State, "open"), now, now)
		return err
	})
}

// Requirement loads one requirement.
func (s *Store) Requirement(ctx context.Context, id string) (Requirement, error) {
	var r Requirement
	var acceptance string
	var created int64
	err := s.db.SQL().QueryRowContext(ctx,
		`SELECT id, title, body, acceptance, state, created_at FROM requirements WHERE id = ?`, id).
		Scan(&r.ID, &r.Title, &r.Body, &acceptance, &r.State, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, fmt.Errorf("task: no requirement %q", id)
	}
	if err != nil {
		return r, err
	}
	r.CreatedAt = time.UnixMilli(created)
	if acceptance != "" {
		_ = json.Unmarshal([]byte(acceptance), &r.Acceptance)
	}
	return r, nil
}

// Create records a task.
func (s *Store) Create(ctx context.Context, t Task) error {
	if t.ID == "" {
		return errors.New("task: task needs an id")
	}
	if t.State == "" {
		t.State = StatePending
	}
	if t.Verification == "" {
		t.Verification = recipe.Standard
	}
	if t.Budget.MaxAttempts == 0 {
		t.Budget.MaxAttempts = DefaultBudget().MaxAttempts
		if t.Budget.MaxWallTime == 0 {
			t.Budget.MaxWallTime = DefaultBudget().MaxWallTime
		}
	}
	budget, err := json.Marshal(t.Budget)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO tasks (id, workspace_id, requirement_id, parent_id, title, kind,
			                   worktree_id, state, verification, budget, created_at, updated_at)
			VALUES (?,?,NULLIF(?,''),NULLIF(?,''),?,?,?,?,?,?,?,?)`,
			t.ID, s.ws.String(), t.RequirementID, t.ParentID, t.Title, orDefault(t.Kind, "change"),
			t.WorktreeID, string(t.State), string(t.Verification), string(budget), now, now)
		return err
	})
}

// Get loads one task.
func (s *Store) Get(ctx context.Context, id string) (Task, error) {
	var t Task
	var reqID, parentID, budget sql.NullString
	var created, updated int64
	var finished sql.NullInt64
	var wsID string

	err := s.db.SQL().QueryRowContext(ctx, `
		SELECT id, workspace_id, requirement_id, parent_id, title, kind, worktree_id,
		       state, verification, budget, created_at, updated_at, finished_at
		FROM tasks WHERE id = ?`, id).
		Scan(&t.ID, &wsID, &reqID, &parentID, &t.Title, &t.Kind, &t.WorktreeID,
			&t.State, &t.Verification, &budget, &created, &updated, &finished)
	if errors.Is(err, sql.ErrNoRows) {
		return t, fmt.Errorf("task: no task %q", id)
	}
	if err != nil {
		return t, err
	}
	t.WorkspaceID = workspace.ID(wsID)
	t.RequirementID, t.ParentID = reqID.String, parentID.String
	t.CreatedAt, t.UpdatedAt = time.UnixMilli(created), time.UnixMilli(updated)
	if finished.Valid {
		f := time.UnixMilli(finished.Int64)
		t.FinishedAt = &f
	}
	if budget.Valid && budget.String != "" {
		_ = json.Unmarshal([]byte(budget.String), &t.Budget)
	}
	return t, nil
}

// List returns tasks, newest first.
func (s *Store) List(ctx context.Context) ([]Task, error) {
	rows, err := s.db.SQL().QueryContext(ctx, `SELECT id FROM tasks ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	var ids []string
	func() {
		defer rows.Close()
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				ids = append(ids, id)
			}
		}
	}()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]Task, 0, len(ids))
	for _, id := range ids {
		t, err := s.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// SetState moves a task and stamps the finish time on a terminal state.
func (s *Store) SetState(ctx context.Context, id string, state State) error {
	now := time.Now().UnixMilli()
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		if state.Terminal() {
			_, err := tx.ExecContext(ctx,
				`UPDATE tasks SET state = ?, updated_at = ?, finished_at = ? WHERE id = ?`,
				string(state), now, now, id)
			return err
		}
		_, err := tx.ExecContext(ctx,
			`UPDATE tasks SET state = ?, updated_at = ?, finished_at = NULL WHERE id = ?`,
			string(state), now, id)
		return err
	})
}

// ErrNotRetryable is returned when a task cannot be reopened.
var ErrNotRetryable = errors.New("task: not retryable")

// Reopen returns a terminal task to pending so it can be run again.
//
// A failed task is not always a task that was tried and could not be done. In
// this repository, three separate harness misconfigurations — an output budget
// too small for the model's reasoning, a request longer than the provider's
// timeout, and memory pressure — each produced a failed task whose work had
// never really been attempted. Fixing the configuration did not help: the
// task refused to run, so its journal, its attempt history and its id were
// abandoned and the same description had to be typed again as a new task.
// That loses the one record of what was tried.
//
// Accepted is refused rather than reopened. Its change has been through the
// completion contract and may already be merged, so running it again would
// redo work someone approved on evidence that no longer describes the
// worktree. Abandoning it explicitly and creating a new task says what is
// actually happening.
//
// The worktree is deliberately left alone. A retry is a continuation, and
// §7.2's premise is that an interrupted task's checkout is inspected rather
// than assumed about — Run reopens an existing worktree, and recovery
// classifies what it finds.
func (s *Store) Reopen(ctx context.Context, id string) (Task, error) {
	t, err := s.Get(ctx, id)
	if err != nil {
		return Task{}, err
	}
	switch {
	case t.State == StateAccepted:
		return t, fmt.Errorf("%w: %s was accepted, and its change may already be applied; "+
			"create a new task rather than redoing approved work", ErrNotRetryable, id)
	case !t.State.Terminal():
		return t, fmt.Errorf("%w: %s is %s, which is already runnable", ErrNotRetryable, id, t.State)
	}
	if err := s.SetState(ctx, id, StatePending); err != nil {
		return t, err
	}
	reopened := t
	reopened.State = StatePending
	return reopened, nil
}

// SetWorktree records which checkout a task owns.
func (s *Store) SetWorktree(ctx context.Context, id, worktreeID string) error {
	return s.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx,
			`UPDATE tasks SET worktree_id = ?, updated_at = ? WHERE id = ?`,
			worktreeID, time.Now().UnixMilli(), id)
		return err
	})
}

// NewID mints a task id from the clock and a short random suffix. Sortable by
// creation, which makes a directory of worktrees readable.
func NewID(prefix string) string {
	return fmt.Sprintf("%s-%d-%s", prefix, time.Now().Unix(), randSuffix(4))
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// Summary is the one-line form of a task list.
func (t Task) Summary() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s [%s] %s", t.ID, t.State, t.Title)
	if t.Verification != "" {
		fmt.Fprintf(&b, " (verify: %s)", t.Verification)
	}
	return b.String()
}

// SortTasks orders tasks for display: live work first, then by recency.
func SortTasks(tasks []Task) {
	rank := map[State]int{
		StateRunning: 0, StateReview: 1, StateBlocked: 2, StatePaused: 3,
		StatePending: 4, StateAccepted: 5, StateFailed: 6, StateAbandoned: 7,
	}
	sort.SliceStable(tasks, func(i, j int) bool {
		if rank[tasks[i].State] != rank[tasks[j].State] {
			return rank[tasks[i].State] < rank[tasks[j].State]
		}
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})
}
