package supervisor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/session"
	"github.com/akynte/local-engineer/internal/supervisor"
	"github.com/akynte/local-engineer/internal/workspace"
)

func bound(t *testing.T) *session.Session {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/x\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Init(dir, workspace.InitOptions{Name: "x"}); err != nil {
		t.Fatal(err)
	}
	s, err := session.Open(context.Background(), t.TempDir(), dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// A task finished without a verification must not read as fine. An executor's
// own account of its work is exactly what the completion contract exists not
// to trust, and a review that quietly said VERIFIED would launder it.
func TestFinishingWithoutVerifyingIsReportedUnverified(t *testing.T) {
	ctx := context.Background()
	s := bound(t)
	task, err := supervisor.StartTask(ctx, s.Store, "do a thing", recipe.Standard)
	if err != nil {
		t.Fatal(err)
	}
	rev, err := supervisor.FinishTask(ctx, s.Store, s.Workspace.Root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rev.Verdict != "UNVERIFIED" {
		t.Errorf("verdict = %q, want UNVERIFIED", rev.Verdict)
	}
	if !strings.Contains(rev.Format(), "nothing checked the work") {
		t.Errorf("the review does not say why it is unverified:\n%s", rev.Format())
	}
}

// A decision the user made is the part git cannot reconstruct, and the reason
// this record exists at all.
func TestUserDecisionsReachTheReview(t *testing.T) {
	ctx := context.Background()
	s := bound(t)
	task, err := supervisor.StartTask(ctx, s.Store, "add rate limiting", recipe.Standard)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RecordAnswer(ctx, s.Store, task.ID,
		"Per IP or per account?", "Per IP, 10 a minute"); err != nil {
		t.Fatal(err)
	}
	rev, err := supervisor.FinishTask(ctx, s.Store, s.Workspace.Root, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(rev.Decisions) != 1 {
		t.Fatalf("got %d decisions, want 1", len(rev.Decisions))
	}
	if rev.Decisions[0].Answer != "Per IP, 10 a minute" {
		t.Errorf("answer = %q", rev.Decisions[0].Answer)
	}
	if !strings.Contains(rev.Format(), "Per IP or per account?") {
		t.Error("the question is missing from the rendered review")
	}
}

// A half-recorded decision is worse than none: it reads as though the user was
// consulted when the answer is unknown.
func TestAnIncompleteDecisionIsRefused(t *testing.T) {
	ctx := context.Background()
	s := bound(t)
	task, err := supervisor.StartTask(ctx, s.Store, "x", recipe.Standard)
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.RecordAnswer(ctx, s.Store, task.ID, "What limit?", ""); err == nil {
		t.Error("a decision with no answer was recorded")
	}
	if err := supervisor.RecordAnswer(ctx, s.Store, task.ID, "", "10"); err == nil {
		t.Error("a decision with no question was recorded")
	}
}

// A task needs an objective: the journal's whole value is saying what was
// being attempted.
func TestATaskNeedsAnObjective(t *testing.T) {
	s := bound(t)
	if _, err := supervisor.StartTask(context.Background(), s.Store, "   ", recipe.Standard); err == nil {
		t.Error("a task with no objective was created")
	}
}
