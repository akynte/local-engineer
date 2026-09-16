package ledger_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/ledger"
)

// The journal records an outcome after the side effect, which means it records
// it exactly when the operation is ending — including when it is ending
// because its own context died. Four runs in one session lost the cause to
// "store: begin on ledger: context deadline exceeded", leaving an uncertain
// operation with nothing beside it to say why.
func TestAnInterruptionIsRecordedEvenWhenTheContextIsAlreadyDead(t *testing.T) {
	l, _ := newLedger(t)
	const taskID = "t-recording"

	h, err := l.Begin(context.Background(), taskID, ledger.KindRecipeRun,
		map[string]any{"kind": "engine_step"}, "before")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	// Exactly the situation runner.go is in: the step returned because its
	// context was cancelled, and that same context is what reaches the journal.
	dead, cancel := context.WithCancel(context.Background())
	cancel()

	cause := errors.New("engine step: context deadline exceeded")
	if err := h.Interrupted(dead, cause); err != nil {
		t.Fatalf("recording an interruption must not depend on the context it is recording: %v", err)
	}

	ops, err := l.Operations(context.Background(), taskID)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected one operation, got %d", len(ops))
	}
	// Uncertain is correct and required: the side effect may have taken hold,
	// so §7.2 must still inspect the worktree rather than trust a verdict.
	if !ops[0].Uncertain() {
		t.Error("an interrupted operation must stay uncertain so recovery inspects it")
	}
	if !strings.Contains(ops[0].Error, "deadline exceeded") {
		t.Errorf("the cause was lost: error field is %q", ops[0].Error)
	}
}

// The counterpart, and the reason the fix above is narrow: Complete must still
// refuse a dead context. Writing an outcome makes an operation *certain*, and
// §7.2 skips inspection for certain operations — so under a cancellation, when
// the side effect is exactly what nobody knows, refusing the write is what
// keeps the operation uncertain and gets the worktree looked at.
func TestCompleteStillRefusesADeadContext(t *testing.T) {
	l, _ := newLedger(t)
	const taskID = "t-recording"

	h, err := l.Begin(context.Background(), taskID, ledger.KindRecipeRun,
		map[string]any{"kind": "verification"}, "before")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	if err := h.Complete(dead, map[string]any{"summary": "done"}, "after", ""); err == nil {
		t.Fatal("an outcome was written under a cancelled context; an interrupted " +
			"operation would read as complete and recovery would never inspect it")
	}

	ops, err := l.Operations(context.Background(), taskID)
	if err != nil {
		t.Fatalf("reading the journal: %v", err)
	}
	if !ops[0].Uncertain() {
		t.Error("the operation must remain uncertain after a refused completion")
	}
}

// An expired deadline is the shape the runner actually produced — a timeout
// rather than an explicit cancel — and it must behave the same way.
func TestAnExpiredDeadlineStillRecords(t *testing.T) {
	l, _ := newLedger(t)
	const taskID = "t-recording"

	h, err := l.Begin(context.Background(), taskID, ledger.KindRecipeRun, map[string]any{}, "")
	if err != nil {
		t.Fatalf("begin: %v", err)
	}

	expired, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Hour))
	defer cancel()

	if err := h.Interrupted(expired, errors.New("timed out")); err != nil {
		t.Fatalf("an expired deadline must not prevent the record: %v", err)
	}
}

// Begin is deliberately NOT detached: there is no point writing the intent of
// an operation whose context is already gone, and doing so would start work the
// caller has abandoned.
func TestBeginStillRespectsACancelledContext(t *testing.T) {
	l, _ := newLedger(t)
	const taskID = "t-recording"

	dead, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := l.Begin(dead, taskID, ledger.KindRecipeRun, map[string]any{}, ""); err == nil {
		t.Error("Begin must refuse a cancelled context: intent is written before the side effect, " +
			"so starting one the caller abandoned is work nobody asked for")
	}
}
