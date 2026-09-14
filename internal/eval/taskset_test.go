package eval_test

// The task set is as much a part of the measurement as the harness. A task
// that cannot be solved, or that a wrong answer passes, silently changes what
// the numbers mean.
//
// Every task here is checked two ways: a known-correct solution must pass, and
// a plausible wrong one must fail. Without the second check a task that always
// passes would look like an easy task rather than a broken one.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/eval"
)

func taskSetDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		candidate := filepath.Join(dir, "evals", "tasks")
		if st, err := os.Stat(candidate); err == nil && st.IsDir() {
			return candidate
		}
		dir = filepath.Dir(dir)
	}
	t.Skip("evals/tasks not found")
	return ""
}

func loadSet(t *testing.T) []eval.Task {
	t.Helper()
	tasks, err := eval.LoadSet(taskSetDir(t))
	if err != nil {
		t.Fatalf("the task set does not load: %v", err)
	}
	return tasks
}

// Every shipped task must validate, or the set silently shrinks.
func TestShippedTaskSetValidates(t *testing.T) {
	tasks := loadSet(t)
	if len(tasks) == 0 {
		t.Fatal("the task set is empty")
	}
	for _, task := range tasks {
		if err := task.Validate(); err != nil {
			t.Errorf("%v", err)
		}
		if task.Notes == "" {
			t.Errorf("%s: no notes; a reader of the results needs to know what a "+
				"wrong-but-passing solution looks like", task.ID)
		}
		if _, err := os.Stat(task.FixturePath()); err != nil {
			t.Errorf("%s: fixture missing: %v", task.ID, err)
		}
	}
}

// The objective must not give away the fix. An objective that says what to
// change measures typing, not engineering.
func TestObjectivesDoNotContainTheAnswer(t *testing.T) {
	giveaways := []string{"x + y", "x - y", "return nil", "ErrNotFound"}
	for _, task := range loadSet(t) {
		lower := strings.ToLower(task.Objective)
		for _, g := range giveaways {
			if strings.Contains(lower, strings.ToLower(g)) {
				t.Errorf("%s: the objective contains %q, which gives away the fix", task.ID, g)
			}
		}
	}
}

// A fixture must start in a state where its own visible tests pass. A fixture
// that is already failing makes the task ambiguous: the solver cannot tell
// which failure it was asked to fix.
func TestFixturesStartGreenOnTheirVisibleTests(t *testing.T) {
	requireGo(t)
	for _, task := range loadSet(t) {
		t.Run(task.ID, func(t *testing.T) {
			dir := t.TempDir()
			copyFixture(t, task.FixturePath(), dir)

			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
				t.Skip("not a Go fixture")
			}
			cmd := exec.Command("go", "test", "./...")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("the fixture's own tests fail before any work is done:\n%s", out)
			}
		})
	}
}

// The acceptance command must fail on the untouched fixture. A task whose
// acceptance already passes measures nothing at all, and would look like a
// task every arm solves.
func TestAcceptanceFailsOnTheUntouchedFixture(t *testing.T) {
	requireGo(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	for _, task := range loadSet(t) {
		t.Run(task.ID, func(t *testing.T) {
			out := r.Run(context.Background(), task, eval.Arm{Name: "noop"},
				solverFunc(func(context.Context, eval.SolveRequest) (eval.SolveResult, error) {
					return eval.SolveResult{}, nil // change nothing
				}))
			if out.Errored() {
				t.Fatalf("run errored: %s", out.Err)
			}
			if out.Solved {
				t.Fatal("the acceptance command passes on the untouched fixture; " +
					"this task measures nothing")
			}
		})
	}
}

// And a known-correct solution must pass, or the task is unsolvable and every
// arm scores zero on it for reasons that have nothing to do with the system.
func TestKnownGoodSolutionsPass(t *testing.T) {
	requireGo(t)
	r := &eval.Runner{WorkDir: t.TempDir(), Logf: t.Logf}

	solutions := map[string]eval.Solver{
		"nil-deref-001":        solverFunc(solveNilDeref),
		"signature-change-001": solverFunc(solveSignatureChange),
		"config-rename-001":    solverFunc(solveConfigRename),
	}

	for _, task := range loadSet(t) {
		solver, ok := solutions[task.ID]
		if !ok {
			t.Errorf("%s has no reference solution; a task nobody has solved "+
				"may be unsolvable, and every arm would score zero for the wrong reason", task.ID)
			continue
		}
		t.Run(task.ID, func(t *testing.T) {
			out := r.Run(context.Background(), task, eval.Arm{Name: "reference"}, solver)
			if out.Errored() {
				t.Fatalf("run errored: %s", out.Err)
			}
			if !out.Solved {
				t.Fatalf("the reference solution does not pass:\n%s", out.AcceptanceOutput)
			}
		})
	}
}

// The reference solutions. Each is what a correct answer looks like; the tests
// above use them to prove the tasks are solvable and the checks discriminate.

func solveNilDeref(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	path := filepath.Join(req.Worktree, "internal/service/user.go")
	body, err := os.ReadFile(path)
	if err != nil {
		return eval.SolveResult{}, err
	}
	fixed := strings.Replace(string(body),
		"	u := s.store.Find(id)\n	return u.Email, nil",
		"	u := s.store.Find(id)\n	if u == nil {\n		return \"\", ErrNotFound\n	}\n	return u.Email, nil", 1)
	return eval.SolveResult{Claimed: true, Attempts: 1},
		os.WriteFile(path, []byte(fixed), 0o644)
}

func solveSignatureChange(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	edits := map[string][2]string{
		"internal/billing/total.go": {
			"func Total(lines []Line) int {\n	total := 0\n	for _, l := range lines {\n		total += l.PenceEach * l.Quantity\n	}\n	return total\n}",
			"func Total(lines []Line, currency string) (int, string) {\n	total := 0\n	for _, l := range lines {\n		total += l.PenceEach * l.Quantity\n	}\n	return total, currency\n}",
		},
		"internal/report/monthly.go": {
			"		sum += billing.Total(lines)",
			"		amount, _ := billing.Total(lines, \"GBP\")\n		sum += amount",
		},
		"internal/api/invoice.go": {
			"	return InvoiceResponse{TotalPence: billing.Total(lines)}",
			"	total, _ := billing.Total(lines, \"GBP\")\n	return InvoiceResponse{TotalPence: total}",
		},
		"internal/billing/total_test.go": {
			"	if got := Total(lines); got != 250 {",
			"	if got, _ := Total(lines, \"GBP\"); got != 250 {",
		},
	}
	for rel, edit := range edits {
		path := filepath.Join(req.Worktree, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		updated := strings.Replace(string(body), edit[0], edit[1], 1)
		if updated == string(body) {
			return eval.SolveResult{}, os.ErrInvalid
		}
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

func solveConfigRename(_ context.Context, req eval.SolveRequest) (eval.SolveResult, error) {
	for _, rel := range []string{"internal/config/config.go", "docker-compose.yml", "Dockerfile"} {
		path := filepath.Join(req.Worktree, filepath.FromSlash(rel))
		body, err := os.ReadFile(path)
		if err != nil {
			return eval.SolveResult{}, err
		}
		updated := strings.ReplaceAll(string(body), "DB_URL", "DATABASE_URL")
		if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
			return eval.SolveResult{}, err
		}
	}
	return eval.SolveResult{Claimed: true, Attempts: 1}, nil
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, body, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}
