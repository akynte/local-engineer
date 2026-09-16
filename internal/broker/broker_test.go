package broker_test

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/broker"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func newBroker(t *testing.T, policy broker.Policy) (*broker.Broker, *store.Store) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/broker/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	return broker.New(st, policy), st
}

func TestDefaultPolicyGatesWhatMatters(t *testing.T) {
	p := broker.DefaultPolicy()

	cases := []struct {
		kind broker.Kind
		ev   broker.Evidence
		want bool
	}{
		{broker.KindImpact, broker.Evidence{BreakingCount: 3}, true},
		{broker.KindImpact, broker.Evidence{BreakingCount: 0}, false},
		{broker.KindOutOfScope, broker.Evidence{OutOfScope: []string{"a.go"}}, true},
		{broker.KindOutOfScope, broker.Evidence{}, false},
		{broker.KindApply, broker.Evidence{}, true},
		// A budget increase is always a person's call: the budget exists so a
		// task cannot decide to keep going.
		{broker.KindBudget, broker.Evidence{}, true},
	}
	for _, c := range cases {
		got, why := p.Needs(c.kind, c.ev)
		if got != c.want {
			t.Errorf("%s with %+v: gated=%v, want %v", c.kind, c.ev, got, c.want)
		}
		if got && why == "" {
			t.Errorf("%s: a gate must say why it opened", c.kind)
		}
	}
}

// Permissive approves everything. It must still be an explicit choice, and
// even then a budget increase stays a person's call.
func TestPermissivePolicyStillGatesBudget(t *testing.T) {
	p := broker.Permissive()
	if gated, _ := p.Needs(broker.KindApply, broker.Evidence{}); gated {
		t.Error("permissive should not gate an apply")
	}
	if gated, _ := p.Needs(broker.KindBudget, broker.Evidence{}); !gated {
		t.Error("extending a budget is never automatic")
	}
}

func TestAnUngatedDecisionRecordsNothing(t *testing.T) {
	ctx := context.Background()
	b, _ := newBroker(t, broker.Permissive())

	g, err := b.Ask(ctx, "t1", broker.KindApply, "apply?", broker.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	if g.Decision != broker.Approved {
		t.Fatalf("decision = %s", g.Decision)
	}
	all, err := b.All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// A ledger full of automatic approvals is noise that makes the real gates
	// harder to find.
	if len(all) != 0 {
		t.Errorf("an ungated decision must not be recorded, got %d gates", len(all))
	}
}

func TestGateBlocksUntilDecidedAndIsJournalled(t *testing.T) {
	ctx := context.Background()
	b, st := newBroker(t, broker.DefaultPolicy())
	seedTask(t, st, "t1")

	g, err := b.Ask(ctx, "t1", broker.KindApply, "Apply this change?", broker.Evidence{
		Summary: "one file changed", Diff: "--- a\n+++ b\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !g.Open() {
		t.Fatalf("a gated decision must wait, got %s", g.Decision)
	}
	if !strings.Contains(g.Question, "policy gates") {
		t.Errorf("the question should carry why it opened: %q", g.Question)
	}

	// The gate is journalled before it blocks, so an interrupted approval is a
	// pending gate on restart rather than a lost one.
	ops, err := ledger.New(st).Operations(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, op := range ops {
		if op.Kind == ledger.KindApproval {
			found = true
			if !op.Uncertain() {
				t.Error("an undecided approval must be an open operation")
			}
		}
	}
	if !found {
		t.Fatal("the gate was not journalled")
	}

	pending, err := b.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != g.ID {
		t.Fatalf("pending = %+v", pending)
	}

	decided, err := b.Decide(ctx, g.ID, broker.Approved, "operator", "diff looks right")
	if err != nil {
		t.Fatal(err)
	}
	if decided.Decision != broker.Approved || decided.DecidedBy != "operator" {
		t.Errorf("decided = %+v", decided)
	}
	if decided.DecidedAt == nil {
		t.Error("a decision must be timestamped")
	}

	// The journalled approval is now closed with the answer.
	ops, _ = ledger.New(st).Operations(ctx, "t1")
	for _, op := range ops {
		if op.Kind == ledger.KindApproval && op.Uncertain() {
			t.Error("the approval operation should be closed once decided")
		}
	}
	if _, err := b.Decide(ctx, g.ID, broker.Rejected, "someone", ""); err == nil {
		t.Error("a decided gate must not be decidable again")
	}
}

// The reasoning is what a gate is worth six months later, not the verdict.
func TestTheNoteSurvives(t *testing.T) {
	ctx := context.Background()
	b, st := newBroker(t, broker.DefaultPolicy())
	seedTask(t, st, "t1")

	g, _ := b.Ask(ctx, "t1", broker.KindApply, "apply?", broker.Evidence{})
	if _, err := b.Decide(ctx, g.ID, broker.Rejected, "operator",
		"this changes the retry semantics; needs a design discussion first"); err != nil {
		t.Fatal(err)
	}
	loaded, err := b.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Note, "retry semantics") {
		t.Errorf("the reasoning must survive, got %q", loaded.Note)
	}
}

// Evidence is assembled by the supervisor, so a person reads the
// deterministic answer rather than a model's account of it.
func TestEvidenceRoundTrips(t *testing.T) {
	ctx := context.Background()
	b, st := newBroker(t, broker.DefaultPolicy())
	seedTask(t, st, "t1")

	imp := graph.Impact{
		Kind:   graph.ChangeSignature,
		Counts: map[graph.Verdict]int{graph.Breaking: 2},
		Caveat: graph.ImpactCaveat,
	}
	g, err := b.Ask(ctx, "t1", broker.KindImpact, "proceed?", broker.Evidence{
		Summary: "signature change", Impact: &imp, BreakingCount: 2,
		OutOfScope: []string{"other.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := b.Get(ctx, g.ID)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := loaded.Decoded()
	if err != nil {
		t.Fatal(err)
	}
	if ev.BreakingCount != 2 || ev.Impact == nil {
		t.Fatalf("evidence lost in the round trip: %+v", ev)
	}
	if ev.Impact.Caveat == "" {
		t.Error("the impact caveat must reach the person deciding")
	}
}

// A person asked to approve a 50,000-line diff is not reviewing it.
func TestAnEnormousDiffIsTruncatedWithAPointer(t *testing.T) {
	ctx := context.Background()
	b, st := newBroker(t, broker.DefaultPolicy())
	seedTask(t, st, "t1")

	g, err := b.Ask(ctx, "t1", broker.KindApply, "apply?", broker.Evidence{
		Diff: strings.Repeat("+ a line of diff\n", 20000),
	})
	if err != nil {
		t.Fatal(err)
	}
	ev, err := g.Decoded()
	if err != nil {
		t.Fatal(err)
	}
	if len(ev.Diff) > broker.MaxDiffInGate+200 {
		t.Fatalf("diff not truncated: %d bytes", len(ev.Diff))
	}
	if !strings.Contains(ev.Diff, "inspect the task worktree") {
		t.Error("a truncated diff must point at where the whole change is")
	}
}

// "Nobody looked" and "a person said no" call for different responses.
func TestExpiryIsDistinctFromRejection(t *testing.T) {
	ctx := context.Background()
	policy := broker.DefaultPolicy()
	policy.Timeout = time.Millisecond
	b, st := newBroker(t, policy)
	seedTask(t, st, "t1")

	g, err := b.Ask(ctx, "t1", broker.KindApply, "apply?", broker.Evidence{})
	if err != nil {
		t.Fatal(err)
	}
	if g.ExpiresAt == nil {
		t.Fatal("a policy timeout must set a deadline")
	}
	time.Sleep(5 * time.Millisecond)

	pending, err := b.Pending(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected the expired gate to be reported, got %d", len(pending))
	}
	if pending[0].Decision != broker.Expired {
		t.Fatalf("decision = %s, want expired", pending[0].Decision)
	}
	if pending[0].Decision == broker.Rejected {
		t.Error("expiry must not be recorded as a rejection")
	}
}

func seedTask(t *testing.T, st *store.Store, id string) {
	t.Helper()
	err := st.Ledger().Tx(context.Background(), func(tx *sql.Tx) error {
		now := time.Now().UnixMilli()
		_, err := tx.Exec(`INSERT INTO tasks (id, workspace_id, title, state, created_at, updated_at)
		                   VALUES (?,?,?,'running',?,?)`, id, st.ID().String(), "t", now, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
}
