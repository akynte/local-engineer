package eval_test

// A benchmark harness with a bug produces confident wrong numbers, which is
// worse than no numbers. These tests exercise the properties the whole
// measurement rests on: that acceptance is hidden, that ground truth is
// separate from the system's own verdict, and that the statistics refuse to
// overclaim.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/eval"
)

// taskSet writes a fixture and a task file, returning the task.
func taskSet(t *testing.T) eval.Task {
	t.Helper()
	dir := t.TempDir()

	fixture := filepath.Join(dir, "fixture")
	write(t, filepath.Join(fixture, "go.mod"), "module example.test/eval\n\ngo 1.26\n")
	write(t, filepath.Join(fixture, "calc.go"),
		"package calc\n\n// Add returns the sum of x and y.\nfunc Add(x, y int) int { return x - y }\n")

	write(t, filepath.Join(dir, "fix.task.yaml"), `
id: add-sign-001
category: bug_fix
objective: Add returns the difference instead of the sum. Make it correct.
fixture: fixture
leak_risk: none
verification: standard
budget:
  max_attempts: 2
  max_wall_seconds: 120
acceptance:
  argv: ["go", "test", "./..."]
  timeout_seconds: 60
  must_not_change: ["hidden_test.go"]
  files:
    hidden_test.go: |
      package calc

      import "testing"

      func TestAddHidden(t *testing.T) {
        if Add(2, 3) != 5 {
          t.Fatalf("Add(2,3) = %d, want 5", Add(2, 3))
        }
      }
`)
	task, err := eval.LoadTask(filepath.Join(dir, "fix.task.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the Go toolchain is not on PATH")
	}
}

// solverFunc adapts a function to the Solver interface.
type solverFunc func(context.Context, eval.SolveRequest) (eval.SolveResult, error)

func (f solverFunc) Solve(ctx context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	return f(ctx, req)
}

// The property everything else rests on: a solver must not be able to see the
// acceptance test. A model that can read the test can satisfy it without
// solving the problem, and that failure looks exactly like success.
func TestAcceptanceFilesAreInvisibleDuringTheRun(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	var sawHidden bool
	var listing []string
	solver := solverFunc(func(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
		_ = filepath.WalkDir(req.Worktree, func(p string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			rel, _ := filepath.Rel(req.Worktree, p)
			listing = append(listing, rel)
			if strings.Contains(rel, "hidden_test") {
				sawHidden = true
			}
			return nil
		})
		return eval.SolveResult{}, nil
	})

	r.Run(context.Background(), task, eval.Arm{Name: "probe"}, solver)

	if sawHidden {
		t.Fatalf("the acceptance test was visible to the solver: %v", listing)
	}
	// And the fixture itself must have been there, or the test is vacuous.
	if !strings.Contains(strings.Join(listing, " "), "calc.go") {
		t.Fatalf("the fixture was not copied in: %v", listing)
	}
}

func TestASolvedTaskIsRecognised(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	out := r.Run(context.Background(), task, eval.Arm{Name: "fixer"},
		fixing("return x - y", "return x + y", true))

	if out.Errored() {
		t.Fatalf("run errored: %s", out.Err)
	}
	if !out.Solved {
		t.Fatalf("a correct fix was not recognised: %s", out.AcceptanceOutput)
	}
	if out.FalseAccept || out.MissedSuccess {
		t.Errorf("claimed and solved agree, so neither flag should be set: %+v", out)
	}
	if out.FilesTouched != 1 {
		t.Errorf("files touched = %d, want 1", out.FilesTouched)
	}
}

// The most damaging failure a tool like this has: claiming success when the
// work is wrong. It gets its own field, not a derivation.
func TestAFalseAcceptanceIsRecorded(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	// A "fix" that is still wrong, asserted as complete.
	out := r.Run(context.Background(), task, eval.Arm{Name: "overconfident"},
		fixing("return x - y", "return x * y", true))

	if out.Solved {
		t.Fatal("a wrong answer was scored as solved")
	}
	if !out.FalseAccept {
		t.Fatal("a solver claiming success on a wrong answer must be recorded as a false acceptance")
	}
	if out.AcceptanceOutput == "" {
		t.Error("a failure must carry the acceptance output, or it is not diagnosable")
	}
}

func TestAMissedSuccessIsRecorded(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	// Correct work, but the solver did not claim it.
	out := r.Run(context.Background(), task, eval.Arm{Name: "modest"},
		fixing("return x - y", "return x + y", false))

	if !out.Solved {
		t.Fatalf("the fix was correct: %s", out.AcceptanceOutput)
	}
	if !out.MissedSuccess {
		t.Fatal("correct work the solver did not claim must be recorded as a missed success")
	}
}

// A task "passed" by deleting the failing test has not been solved. Without
// this check the harness would score it as a success.
func TestTamperingWithProtectedFilesCountsAsUnsolved(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	// The solver pre-creates the protected path, which the acceptance step
	// then overwrites — so the test itself would pass, but the tamper check
	// must still fail the task.
	solver := solverFunc(func(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
		if err := os.WriteFile(filepath.Join(req.Worktree, "hidden_test.go"),
			[]byte("package calc\n"), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
		body := "package calc\n\nfunc Add(x, y int) int { return x + y }\n"
		if err := os.WriteFile(filepath.Join(req.Worktree, "calc.go"), []byte(body), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
		return eval.SolveResult{Claimed: true, Attempts: 1}, nil
	})

	out := r.Run(context.Background(), task, eval.Arm{Name: "tamperer"}, solver)

	if !out.Tampered {
		t.Fatalf("touching a protected path must be detected; touched %v", out.TamperedWith)
	}
	if out.Solved {
		t.Fatal("a task passed by touching a protected file must not be scored as solved")
	}
	if !out.FalseAccept {
		t.Error("the solver claimed success, so this is also a false acceptance")
	}
}

// A harness fault is not evidence about the system. Counting a solver error as
// a failure would understate it.
func TestASolverErrorIsNotAFailedTask(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	out := r.Run(context.Background(), task, eval.Arm{Name: "broken"},
		solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
			return eval.SolveResult{}, context.Canceled
		}))

	if !out.Errored() {
		t.Fatal("a solver error must be recorded as an error")
	}
	if out.Solved || out.FalseAccept {
		t.Error("an errored run must not contribute a verdict")
	}

	// And aggregation must exclude it from the rates.
	rep := eval.Aggregate([]eval.Outcome{out}, []eval.Task{task})
	if len(rep.Arms) != 1 {
		t.Fatal(rep.Arms)
	}
	if rep.Arms[0].Solved.Total != 0 {
		t.Errorf("an errored run was counted in the rate denominator: %+v", rep.Arms[0].Solved)
	}
	if rep.Arms[0].Errored != 1 {
		t.Errorf("errored count = %d", rep.Arms[0].Errored)
	}
}

// fixing returns a solver that applies a replacement and reports a verdict.
func fixing(old, new string, claim bool) eval.Solver {
	return solverFunc(func(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
		path := filepath.Join(req.Worktree, "calc.go")
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		updated := strings.Replace(string(body), old, new, 1)
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
		return eval.SolveResult{Claimed: claim, Attempts: 1, Tokens: 100}, nil
	})
}

// A task set may have been written by someone else. An acceptance file path
// that escapes the scratch copy must be refused, not written.
func TestAcceptancePathsCannotEscapeTheScratchCopy(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	// A task file claiming to write outside its own directory.
	task.Acceptance.Files = map[string]string{
		"../escaped.go": "package x\n",
	}
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	out := r.Run(context.Background(), task, eval.Arm{Name: "probe"},
		solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
			return eval.SolveResult{}, nil
		}))

	if !out.Errored() {
		t.Fatal("an acceptance path escaping the scratch copy must fail the run")
	}
	if !strings.Contains(out.Err, "outside the worktree") {
		t.Errorf("the error should name the confinement: %q", out.Err)
	}
	if out.Solved {
		t.Error("an errored run must not report a verdict")
	}
}

// The operator stopping a run must not become a data point.
func TestAnInterruptedRunIsNotAVerdict(t *testing.T) {
	requireGo(t)
	task := taskSet(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	out := r.Run(context.Background(), task, eval.Arm{Name: "stopped"},
		solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
			return eval.SolveResult{}, context.Canceled
		}))

	if !out.Errored() {
		t.Fatal("an interrupted run must be recorded as an error, not a failure to solve")
	}
	if out.Err != "interrupted" {
		t.Errorf("err = %q", out.Err)
	}
	rep := eval.Aggregate([]eval.Outcome{out}, []eval.Task{task})
	if rep.Arms[0].Solved.Total != 0 {
		t.Error("an interrupted run was counted in the rate denominator")
	}
}
