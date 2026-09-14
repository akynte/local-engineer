package ledger

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Applied classifies an uncertain operation after inspection (§7.2 step 1):
// "file exists and matches intent hash: mark complete; partially matches: mark
// partial; absent: mark not applied".
type Applied string

const (
	AppliedComplete   Applied = "complete"
	AppliedPartial    Applied = "partial"
	AppliedNotApplied Applied = "not_applied"
	// AppliedUnknown is used when the operation has no inspectable side
	// effect (a search, a retrieval). Such an operation is safe to repeat.
	AppliedUnknown Applied = "unknown"
)

// EditIntent is the intent payload of a KindEdit operation. Recovery needs a
// declared shape to inspect against, so edits must journal these fields.
type EditIntent struct {
	Path string `json:"path"`
	// AfterHash is the sha256 the file is expected to hold once the edit has
	// been applied. It is what makes an uncertain edit decidable.
	AfterHash string `json:"after_hash"`
	// BeforeHash is the sha256 the file held before the edit.
	BeforeHash string `json:"before_hash"`
	Summary    string `json:"summary,omitempty"`
}

// Reconciliation is the verdict for one uncertain operation.
type Reconciliation struct {
	Operation Operation `json:"operation"`
	Applied   Applied   `json:"applied"`
	Detail    string    `json:"detail"`
}

// EvidenceStatus pairs an evidence id with whether it still applies to the
// current candidate. §7.2 step 2: "evidence for an older candidate is marked
// stale".
type EvidenceStatus struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Candidate string `json:"candidate"`
	Stale     bool   `json:"stale"`
}

// WorkingState is the reconstruction required by §7.2 step 2.
type WorkingState struct {
	TaskID string `json:"task_id"`
	// Objective is the requirement the task serves.
	Objective string `json:"objective"`
	// InvestigatedFiles and InvestigatedSymbols come from inspect_file,
	// search and retrieval operations.
	InvestigatedFiles   []string `json:"investigated_files"`
	InvestigatedSymbols []string `json:"investigated_symbols"`
	// Decisions are accepted hypotheses; RejectedHypotheses carry the evidence
	// ids that ruled them out, so a resumed session does not re-try them.
	Decisions          []json.RawMessage `json:"decisions"`
	RejectedHypotheses []json.RawMessage `json:"rejected_hypotheses"`
	CompletedEdits     []EditIntent      `json:"completed_edits"`
	Validations        []EvidenceStatus  `json:"validations"`
	RemainingPlan      []string          `json:"remaining_plan"`
	NextAction         string            `json:"next_action"`

	// CurrentCandidate is the worktree's content manifest right now.
	CurrentCandidate string `json:"current_candidate"`
	// LastCompletedCandidate is candidate_after of the last completed
	// operation; a mismatch with CurrentCandidate means the worktree drifted.
	LastCompletedCandidate string `json:"last_completed_candidate"`
	Drifted                bool   `json:"drifted"`

	Uncertain   []Reconciliation `json:"uncertain"`
	CheckpointS int64            `json:"checkpoint_seq"`
	RecoveredAt time.Time        `json:"recovered_at"`
}

// SafeToResume reports whether a fresh session can be seeded from this state
// without re-checking. §7.2 step 3 forbids replaying a recipe or an edit whose
// outcome is uncertain without re-checking the candidate.
func (w WorkingState) SafeToResume() bool {
	for _, u := range w.Uncertain {
		if u.Applied == AppliedPartial {
			return false
		}
	}
	return !w.Drifted
}

// TerminalStates are task states that recovery skips.
var TerminalStates = []string{"accepted", "failed", "abandoned"}

// Recover runs the §7.2 procedure for every task not in a terminal state.
// worktreePath resolves a task's worktree so its content manifest can be
// computed; it may return "" for tasks with no worktree yet.
func (l *Ledger) Recover(ctx context.Context, worktreePath func(taskID string) string) ([]WorkingState, error) {
	ph := make([]string, len(TerminalStates))
	args := make([]any, len(TerminalStates))
	for i, s := range TerminalStates {
		ph[i], args[i] = "?", s
	}
	rows, err := l.db.SQL().QueryContext(ctx,
		`SELECT id, title, COALESCE(requirement_id,'') FROM tasks WHERE state NOT IN (`+
			strings.Join(ph, ",")+`) ORDER BY created_at`, args...)
	if err != nil {
		return nil, fmt.Errorf("ledger: list live tasks: %w", err)
	}
	type row struct{ id, title, req string }
	var live []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.title, &r.req); err != nil {
			rows.Close()
			return nil, err
		}
		live = append(live, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var out []WorkingState
	for _, r := range live {
		ws, err := l.RecoverTask(ctx, r.id, worktreePath(r.id))
		if err != nil {
			return nil, err
		}
		if ws.Objective == "" {
			ws.Objective = r.title
		}
		out = append(out, ws)
	}
	return out, nil
}

// RecoverTask reconstructs one task's working state.
func (l *Ledger) RecoverTask(ctx context.Context, taskID, worktree string) (WorkingState, error) {
	st := WorkingState{TaskID: taskID, RecoveredAt: time.Now().UTC()}

	ops, err := l.Operations(ctx, taskID)
	if err != nil {
		return st, err
	}

	current := ""
	if worktree != "" {
		current, err = ContentManifest(worktree)
		if err != nil {
			return st, fmt.Errorf("ledger: manifest %s: %w", worktree, err)
		}
	}
	st.CurrentCandidate = current

	// Step 1: reconcile. Compare the worktree's manifest with candidate_after
	// of the last completed operation, and classify every NULL outcome.
	for _, op := range ops {
		if !op.Uncertain() && op.CandidateAfter != "" {
			st.LastCompletedCandidate = op.CandidateAfter
		}
		if op.Uncertain() {
			st.Uncertain = append(st.Uncertain, inspect(op, worktree))
		}
	}
	st.Drifted = st.LastCompletedCandidate != "" && current != "" && st.LastCompletedCandidate != current

	// Step 2: reconstruct from the last checkpoint plus completed operations.
	seq, _, handoff, _, err := l.LastCheckpoint(ctx, taskID)
	if err != nil {
		return st, err
	}
	st.CheckpointS = seq
	if len(handoff) > 0 {
		var h struct {
			Objective  string   `json:"objective"`
			Remaining  []string `json:"remaining_plan"`
			NextAction string   `json:"next_action"`
		}
		if err := json.Unmarshal(handoff, &h); err == nil {
			st.Objective, st.RemainingPlan, st.NextAction = h.Objective, h.Remaining, h.NextAction
		}
	}

	files := map[string]bool{}
	symbols := map[string]bool{}
	for _, op := range ops {
		switch op.Kind {
		case KindInspectFile:
			var v struct {
				Path string `json:"path"`
			}
			if json.Unmarshal(op.Intent, &v) == nil && v.Path != "" {
				files[v.Path] = true
			}
		case KindSearch, KindRetrieval:
			var v struct {
				Symbols []string `json:"symbols"`
				Query   string   `json:"query"`
			}
			if json.Unmarshal(op.Intent, &v) == nil {
				for _, s := range v.Symbols {
					symbols[s] = true
				}
			}
		case KindDecision:
			var v struct {
				Accepted bool `json:"accepted"`
			}
			_ = json.Unmarshal(op.Intent, &v)
			if v.Accepted {
				st.Decisions = append(st.Decisions, op.Intent)
			} else {
				st.RejectedHypotheses = append(st.RejectedHypotheses, op.Intent)
			}
		case KindEdit:
			if op.Uncertain() {
				continue
			}
			var ei EditIntent
			if json.Unmarshal(op.Intent, &ei) == nil && ei.Path != "" {
				st.CompletedEdits = append(st.CompletedEdits, ei)
			}
		}
	}
	st.InvestigatedFiles = sortedKeys(files)
	st.InvestigatedSymbols = sortedKeys(symbols)

	// Validations already run, with evidence for an older candidate marked stale.
	st.Validations, err = l.evidenceFor(ctx, taskID, current)
	if err != nil {
		return st, err
	}

	if st.NextAction == "" {
		st.NextAction = nextAction(st)
	}
	return st, nil
}

func nextAction(st WorkingState) string {
	for _, u := range st.Uncertain {
		if u.Applied == AppliedPartial {
			return "inspect the partially applied operation at seq " +
				fmt.Sprint(u.Operation.Seq) + " and reconcile the worktree before continuing"
		}
	}
	if st.Drifted {
		return "re-run validation: the worktree no longer matches the last completed operation"
	}
	for _, v := range st.Validations {
		if v.Stale {
			return "re-run stale validations against the current candidate"
		}
	}
	if len(st.RemainingPlan) > 0 {
		return st.RemainingPlan[0]
	}
	return "re-plan from the reconstructed state"
}

// inspect implements the classification of §7.2 step 1.
func inspect(op Operation, worktree string) Reconciliation {
	r := Reconciliation{Operation: op, Applied: AppliedUnknown}
	switch op.Kind {
	case KindEdit:
	case KindRecipeRun:
		r.Detail = "recipe run with no recorded outcome; re-check the candidate before replaying (§7.2 step 3)"
		return r
	default:
		r.Detail = "read-only operation; safe to repeat"
		return r
	}

	var ei EditIntent
	if err := json.Unmarshal(op.Intent, &ei); err != nil || ei.Path == "" {
		r.Detail = "edit intent did not declare a path; cannot classify"
		return r
	}
	if worktree == "" {
		r.Detail = "no worktree available to inspect"
		return r
	}
	full := filepath.Join(worktree, filepath.FromSlash(ei.Path))
	got, err := hashFile(full)
	if err != nil {
		if os.IsNotExist(err) {
			r.Applied = AppliedNotApplied
			r.Detail = "target file is absent"
			return r
		}
		r.Detail = "could not read target file: " + err.Error()
		return r
	}
	switch got {
	case ei.AfterHash:
		r.Applied = AppliedComplete
		r.Detail = "file matches the intent's after-hash"
	case ei.BeforeHash:
		r.Applied = AppliedNotApplied
		r.Detail = "file still matches the intent's before-hash"
	default:
		r.Applied = AppliedPartial
		r.Detail = "file matches neither the before- nor the after-hash"
	}
	return r
}

func (l *Ledger) evidenceFor(ctx context.Context, taskID, candidate string) ([]EvidenceStatus, error) {
	rows, err := l.db.SQL().QueryContext(ctx,
		`SELECT id, kind, status, candidate FROM evidence WHERE task_id = ? ORDER BY created_at`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EvidenceStatus
	for rows.Next() {
		var e EvidenceStatus
		if err := rows.Scan(&e.ID, &e.Kind, &e.Status, &e.Candidate); err != nil {
			return nil, err
		}
		e.Stale = candidate != "" && e.Candidate != "" && e.Candidate != candidate
		out = append(out, e)
	}
	return out, rows.Err()
}

// RecordEvidence stores a validation result against the candidate it was
// produced for, which is what lets recovery mark it stale later.
func (l *Ledger) RecordEvidence(ctx context.Context, id, taskID, kind, status, candidate, artifactHash, summary string, detail any) error {
	body, err := json.Marshal(detail)
	if err != nil {
		return err
	}
	return l.db.Tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			INSERT INTO evidence (id, task_id, kind, status, candidate, artifact_hash, summary, detail, created_at)
			VALUES (?,?,?,?,?,?,?,?,?)
			ON CONFLICT (id) DO UPDATE SET status = excluded.status, summary = excluded.summary,
			  detail = excluded.detail, candidate = excluded.candidate`,
			id, taskID, kind, status, candidate, artifactHash, summary, string(body), time.Now().UnixMilli())
		return err
	})
}

// ContentManifest hashes a worktree into the candidate identifier used by the
// journal. It is a hash of (relative path, content hash) pairs in sorted
// order, so it is stable across runs and detects any change to any tracked
// file. `.git` and `.le` are excluded: neither is part of the candidate.
func ContentManifest(root string) (string, error) {
	type entry struct{ path, hash string }
	var entries []entry
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".le", "node_modules", "vendor":
				if p != root {
					return fs.SkipDir
				}
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		h, err := hashFile(p)
		if err != nil {
			return err
		}
		entries = append(entries, entry{filepath.ToSlash(rel), h})
		return nil
	})
	if err != nil {
		return "", err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	sum := sha256.New()
	for _, e := range entries {
		io.WriteString(sum, e.path)
		sum.Write([]byte{0})
		io.WriteString(sum, e.hash)
		sum.Write([]byte{0})
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile exposes the file digest used by edit intents, so callers record the
// same hash recovery will compare against.
func HashFile(path string) (string, error) { return hashFile(path) }

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
