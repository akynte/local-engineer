package ledger_test

// Recovery tests for design v3 §7.2. Each interruption class from the Stage D
// list is represented by the journal state it leaves behind: an operation with
// an intent and no outcome, plus a worktree in one of three conditions.

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newLedger(t *testing.T) (*ledger.Ledger, *store.Store) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	id := workspace.DeriveID("/synthetic/root", "", "recovery-test")
	st, err := root.OpenWorkspace(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return ledger.New(st), st
}

func seedTask(t *testing.T, st *store.Store, taskID string) {
	t.Helper()
	err := st.Ledger().Tx(context.Background(), func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO tasks (id, workspace_id, title, state, created_at, updated_at)
		                   VALUES (?,?,?,'running',?,?)`, taskID, st.ID().String(), "fix the bug", now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}

// worktree writes a file and returns the directory plus the file's hash.
func worktree(t *testing.T, body string) (dir, hash string) {
	t.Helper()
	dir = t.TempDir()
	p := filepath.Join(dir, "main.go")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	h, err := ledger.HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return dir, h
}

func TestIntentIsWrittenBeforeOutcome(t *testing.T) {
	ctx := context.Background()
	l, st := newLedger(t)
	seedTask(t, st, "t1")

	h, err := l.Begin(ctx, "t1", ledger.KindEdit, ledger.EditIntent{Path: "main.go", AfterHash: "abc"}, "cand0")
	if err != nil {
		t.Fatal(err)
	}
	ops, err := l.Operations(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 || !ops[0].Uncertain() {
		t.Fatalf("an operation must be visible and uncertain before its outcome is written: %+v", ops)
	}
	if err := h.Complete(ctx, map[string]string{"status": "ok"}, "cand1", "ev1"); err != nil {
		t.Fatal(err)
	}
	ops, _ = l.Operations(ctx, "t1")
	if ops[0].Uncertain() {
		t.Fatal("operation is still uncertain after Complete")
	}
	if ops[0].CandidateAfter != "cand1" {
		t.Fatalf("candidate_after = %q", ops[0].CandidateAfter)
	}
}

// §7.2 step 1: an uncertain edit is classified by inspecting the worktree.
func TestUncertainEditClassification(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		final     string // what the worktree holds at recovery time
		want      ledger.Applied
		safeToRun bool
	}{
		{"crash after the write landed", "AFTER", ledger.AppliedComplete, true},
		{"crash before the write landed", "BEFORE", ledger.AppliedNotApplied, true},
		{"crash mid-write", "PART", ledger.AppliedPartial, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l, st := newLedger(t)
			seedTask(t, st, "t1")

			dir, beforeHash := worktree(t, "BEFORE")
			path := filepath.Join(dir, "main.go")
			if err := os.WriteFile(path, []byte("AFTER"), 0o644); err != nil {
				t.Fatal(err)
			}
			afterHash, err := ledger.HashFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(tc.final), 0o644); err != nil {
				t.Fatal(err)
			}

			// The intent is journalled; the process then dies before the
			// outcome is written (kill -9, timeout, hardware failure: the
			// journal state is identical for all of them).
			if _, err := l.Begin(ctx, "t1", ledger.KindEdit,
				ledger.EditIntent{Path: "main.go", BeforeHash: beforeHash, AfterHash: afterHash}, ""); err != nil {
				t.Fatal(err)
			}

			ws, err := l.RecoverTask(ctx, "t1", dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(ws.Uncertain) != 1 {
				t.Fatalf("expected exactly one uncertain operation, got %d", len(ws.Uncertain))
			}
			if got := ws.Uncertain[0].Applied; got != tc.want {
				t.Errorf("classification = %s, want %s (%s)", got, tc.want, ws.Uncertain[0].Detail)
			}
			if ws.SafeToResume() != tc.safeToRun {
				t.Errorf("SafeToResume = %v, want %v", ws.SafeToResume(), tc.safeToRun)
			}
			if ws.NextAction == "" {
				t.Error("recovery must always name a next action")
			}
		})
	}
}

// §7.2 step 2: evidence for an older candidate is marked stale.
func TestEvidenceForAnOlderCandidateIsStale(t *testing.T) {
	ctx := context.Background()
	l, st := newLedger(t)
	seedTask(t, st, "t1")

	dir, _ := worktree(t, "v1")
	candidate1, err := ledger.ContentManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.RecordEvidence(ctx, "ev1", "t1", "test", "pass", candidate1, "", "go test ./... ok", nil); err != nil {
		t.Fatal(err)
	}

	// The worktree moves on.
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("v2"), 0o644); err != nil {
		t.Fatal(err)
	}

	ws, err := l.RecoverTask(ctx, "t1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.Validations) != 1 {
		t.Fatalf("expected one validation, got %d", len(ws.Validations))
	}
	if !ws.Validations[0].Stale {
		t.Error("evidence produced against an older candidate must be marked stale")
	}

	// And evidence for the *current* candidate must not be stale.
	candidate2, _ := ledger.ContentManifest(dir)
	if err := l.RecordEvidence(ctx, "ev2", "t1", "test", "pass", candidate2, "", "ok", nil); err != nil {
		t.Fatal(err)
	}
	ws, _ = l.RecoverTask(ctx, "t1", dir)
	for _, v := range ws.Validations {
		if v.ID == "ev2" && v.Stale {
			t.Error("evidence for the current candidate was wrongly marked stale")
		}
	}
}

// §7.2 step 2: rejected hypotheses survive so a resumed session does not
// re-try them (§10.1 "persistent cross-attempt state").
func TestRejectedHypothesesSurviveRecovery(t *testing.T) {
	ctx := context.Background()
	l, st := newLedger(t)
	seedTask(t, st, "t1")
	dir, _ := worktree(t, "x")

	for _, d := range []struct {
		accepted bool
		text     string
	}{
		{false, "the nil pointer comes from the cache layer"},
		{true, "the nil pointer comes from an unchecked map lookup"},
	} {
		h, err := l.Begin(ctx, "t1", ledger.KindDecision,
			map[string]any{"accepted": d.accepted, "hypothesis": d.text}, "")
		if err != nil {
			t.Fatal(err)
		}
		if err := h.Complete(ctx, map[string]string{"status": "recorded"}, "", "ev-x"); err != nil {
			t.Fatal(err)
		}
	}

	ws, err := l.RecoverTask(ctx, "t1", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ws.RejectedHypotheses) != 1 {
		t.Errorf("expected 1 rejected hypothesis, got %d", len(ws.RejectedHypotheses))
	}
	if len(ws.Decisions) != 1 {
		t.Errorf("expected 1 accepted decision, got %d", len(ws.Decisions))
	}
}

// §7.2 step 4: a crashed holder's lease expires; a new instance takes it.
// Two instances never write the same worktree.
func TestLeaseExcludesASecondHolderUntilExpiry(t *testing.T) {
	ctx := context.Background()
	l, st := newLedger(t)
	seedTask(t, st, "t1")

	if _, err := l.AcquireLease(ctx, "wt1", "t1", "instance-a", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AcquireLease(ctx, "wt1", "t2", "instance-b", time.Minute); err == nil {
		t.Fatal("a second instance acquired a live lease on the same worktree")
	}
	// The holder may renew its own lease.
	if _, err := l.AcquireLease(ctx, "wt1", "t1", "instance-a", time.Minute); err != nil {
		t.Fatalf("holder could not renew its own lease: %v", err)
	}

	// Simulate a crash: the lease is left behind and expires.
	if _, err := l.AcquireLease(ctx, "wt1", "t1", "instance-a", time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := l.AcquireLease(ctx, "wt1", "t2", "instance-b", time.Minute); err != nil {
		t.Fatalf("expired lease was not reclaimable: %v", err)
	}
}

// Recover walks every non-terminal task.
func TestRecoverSkipsTerminalTasks(t *testing.T) {
	ctx := context.Background()
	l, st := newLedger(t)
	seedTask(t, st, "live")

	err := st.Ledger().Tx(ctx, func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO tasks (id, workspace_id, title, state, created_at, updated_at)
		                   VALUES (?,?,?,'accepted',?,?)`, "done", st.ID().String(), "already accepted", now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	dir, _ := worktree(t, "x")
	states, err := l.Recover(ctx, func(string) string { return dir })
	if err != nil {
		t.Fatal(err)
	}
	if len(states) != 1 || states[0].TaskID != "live" {
		t.Fatalf("expected only the live task, got %+v", states)
	}
	if states[0].Objective != "fix the bug" {
		t.Errorf("objective = %q, want the task title", states[0].Objective)
	}
}
