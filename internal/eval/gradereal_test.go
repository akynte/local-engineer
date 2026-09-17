package eval_test

import (
	"context"
	"testing"

	"github.com/akynte/local-engineer/internal/eval"
)

// Grading is parsed out of what `go test` actually prints, so it has to be
// checked against real output rather than against a handwritten sample. The
// untouched warehouse fixture fails three of discount-floor's four hidden
// tests and passes the fourth, which a binary outcome records as a bare zero.
func TestGradingAgainstRealGoTestOutput(t *testing.T) {
	requireGo(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}
	for _, task := range loadSet(t) {
		if task.ID != "discount-floor-001" {
			continue
		}
		out := r.Run(context.Background(), task, eval.Arm{Name: "none"},
			solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
				return eval.SolveResult{Claimed: false, Attempts: 1}, nil
			}))
		if out.Errored() {
			t.Fatalf("run errored: %s", out.Err)
		}
		if out.Solved {
			t.Fatal("the untouched fixture solved the task")
		}
		if !out.Grade.Recorded() {
			t.Fatal("a run of a task with four hidden tests recorded no grade")
		}
		if out.Grade.Total != 4 {
			t.Errorf("total = %d, want 4", out.Grade.Total)
		}
		if out.Grade.Passed != 1 {
			t.Errorf("passed = %d, want 1 (only the ordinary-pricing test holds on the "+
				"untouched fixture); failed = %v", out.Grade.Passed, out.Grade.Failed)
		}
		return
	}
	t.Fatal("discount-floor-001 not found in the task set")
}
