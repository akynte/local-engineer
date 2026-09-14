package eval

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/worktree"
)

// Outcome is one task run under one arm.
type Outcome struct {
	TaskID   string   `json:"task_id"`
	Category Category `json:"category"`
	Arm      string   `json:"arm"`
	LeakRisk LeakRisk `json:"leak_risk"`

	// Solved is the ground truth: the hidden acceptance command passed. This
	// is the only field that measures whether the task was done.
	Solved bool `json:"solved"`
	// Claimed is what the system itself concluded from the evidence it
	// gathered. For the unsupervised arm it is whatever the model asserted.
	Claimed bool `json:"claimed"`
	// FalseAccept is Claimed && !Solved: the system said it was finished and
	// was wrong. It is the most damaging failure a tool like this has, so it
	// is a field rather than something a reader has to derive.
	FalseAccept bool `json:"false_accept"`
	// MissedSuccess is !Claimed && Solved: the work was right but the system
	// did not recognise it. Far less harmful, but it means the contract is
	// too strict somewhere.
	MissedSuccess bool `json:"missed_success"`

	// Tampered records that the solution changed something it was told not to.
	// A task "passed" by deleting the failing test is not solved, and without
	// this check the harness would score it as a success.
	Tampered     bool     `json:"tampered"`
	TamperedWith []string `json:"tampered_with,omitempty"`

	Attempts     int           `json:"attempts"`
	Duration     time.Duration `json:"duration"`
	TokensUsed   int           `json:"tokens_used,omitempty"`
	Diff         string        `json:"-"`
	DiffBytes    int           `json:"diff_bytes"`
	FilesTouched int           `json:"files_touched"`

	// Reasons is what the system said about its own verdict.
	Reasons []string `json:"reasons,omitempty"`
	// AcceptanceOutput is the hidden command's output when it failed, which
	// is what makes a failure diagnosable rather than just a zero.
	AcceptanceOutput string `json:"acceptance_output,omitempty"`
	// Err records a run that could not be completed at all. An error is not a
	// failure to solve the task: a harness fault must never be counted as
	// evidence about the system.
	Err string `json:"error,omitempty"`
}

// Errored reports whether the run itself failed, as opposed to the task not
// being solved.
func (o Outcome) Errored() bool { return o.Err != "" }

// Solver runs one task attempt under one arm, leaving its result in the
// worktree. It is an interface so the harness can be tested without a model,
// and so an arm can be implemented by something other than this codebase.
type Solver interface {
	// Solve attempts the objective in the given worktree.
	Solve(ctx context.Context, req SolveRequest) (SolveResult, error)
}

// SolveRequest is what a solver is given.
type SolveRequest struct {
	Task     Task
	Arm      Arm
	Worktree string
	// Deadline is the wall-clock bound the budget allows.
	Deadline time.Time
}

// SolveResult is what a solver reports. None of it is trusted as ground truth;
// the hidden acceptance command decides that.
type SolveResult struct {
	// Claimed is the solver's own verdict.
	Claimed  bool
	Attempts int
	Tokens   int
	Reasons  []string
}

// Runner executes tasks against solvers.
type Runner struct {
	// WorkDir is where task copies are made. Each run gets its own directory.
	WorkDir string
	// Logf reports progress. Nil discards it.
	Logf func(format string, args ...any)
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Run executes one task under one arm.
//
// The sequence is the whole point of the harness:
//
//  1. copy the fixture to a scratch directory — the acceptance files are NOT
//     among them;
//  2. let the solver work, bounded by the budget;
//  3. record what changed;
//  4. copy the acceptance files in and run the hidden command;
//  5. compare the ground truth with what the solver claimed.
//
// Step 4 happening after step 2 is what makes the measurement mean anything.
func (r *Runner) Run(ctx context.Context, task Task, arm Arm, solver Solver) Outcome {
	out := Outcome{TaskID: task.ID, Category: task.Category, Arm: arm.Name, LeakRisk: task.LeakRisk}
	start := time.Now()

	work, err := r.prepare(task, arm)
	if err != nil {
		out.Err = fmt.Sprintf("preparing the fixture: %v", err)
		return out
	}
	defer func() {
		if err := os.RemoveAll(work); err != nil {
			r.logf("eval: removing %s: %v", work, err)
		}
	}()

	before, err := snapshot(work)
	if err != nil {
		out.Err = fmt.Sprintf("snapshotting the fixture: %v", err)
		return out
	}

	deadline := start.Add(task.Budget.WallClock())
	solveCtx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	res, solveErr := solver.Solve(solveCtx, SolveRequest{
		Task: task, Arm: arm, Worktree: work, Deadline: deadline,
	})
	out.Duration = time.Since(start)
	out.Attempts, out.TokensUsed = res.Attempts, res.Tokens

	switch {
	case errors.Is(solveErr, context.Canceled):
		// The operator stopped the run. Recording a verdict would put a
		// fabricated data point in the results.
		out.Err = "interrupted"
		return out
	case solveErr != nil && !errors.Is(solveErr, context.DeadlineExceeded):
		// A solver fault is a harness-level error, not evidence that the task
		// is hard. Counting it as a failure would understate the system.
		out.Err = fmt.Sprintf("solver: %v", solveErr)
		return out
	}
	out.Claimed = res.Claimed
	out.Reasons = res.Reasons

	after, err := snapshot(work)
	if err != nil {
		out.Err = fmt.Sprintf("snapshotting the result: %v", err)
		return out
	}
	changed := changedFiles(before, after)
	out.FilesTouched = len(changed)

	// A solution that changed something it was told to leave alone has not
	// solved the task, whatever the acceptance command then says.
	out.TamperedWith = intersect(changed, task.Acceptance.MustNotChange)
	out.Tampered = len(out.TamperedWith) > 0

	solved, output, err := r.judge(ctx, task, work)
	if err != nil {
		out.Err = fmt.Sprintf("running the acceptance command: %v", err)
		return out
	}
	out.Solved = solved && !out.Tampered
	if !out.Solved {
		out.AcceptanceOutput = truncate(output, 4000)
	}

	out.FalseAccept = out.Claimed && !out.Solved
	out.MissedSuccess = !out.Claimed && out.Solved
	return out
}

// prepare copies the fixture into a fresh directory.
func (r *Runner) prepare(task Task, arm Arm) (string, error) {
	base := r.WorkDir
	if base == "" {
		base = os.TempDir()
	}
	if err := os.MkdirAll(base, 0o750); err != nil {
		return "", err
	}
	work, err := os.MkdirTemp(base, "eval-"+sanitise(task.ID)+"-"+sanitise(arm.Name)+"-")
	if err != nil {
		return "", err
	}
	if err := copyTree(task.FixturePath(), work); err != nil {
		return "", err
	}
	return work, nil
}

// judge applies the hidden acceptance files and runs the command.
//
// The files are written to the same worktree the solver used, after it has
// finished. Copying to a third location would be tidier but would not test the
// thing that matters: whether the solver's actual output satisfies the test.
func (r *Runner) judge(ctx context.Context, task Task, work string) (bool, string, error) {
	// The paths come from a task file, and a task set may have been written
	// by someone else. Confining them is the same problem the engine has with
	// model-supplied paths, so it uses the same confinement.
	for rel, body := range task.Acceptance.Files {
		if err := worktree.WriteWithin(work, rel, []byte(body)); err != nil {
			return false, "", fmt.Errorf("acceptance file %s: %w", rel, err)
		}
	}

	runCtx, cancel := context.WithTimeout(ctx, task.Acceptance.Timeout())
	defer cancel()

	//nolint:gosec // the command comes from the task file, which is part of the evaluation set
	cmd := exec.CommandContext(runCtx, task.Acceptance.Argv[0], task.Acceptance.Argv[1:]...)
	cmd.Dir = work
	cmd.Env = append(os.Environ(),
		"GOFLAGS=-mod=mod", "GOPROXY=off", "GOTOOLCHAIN=local",
		"NO_COLOR=1", "TERM=dumb")

	output, err := cmd.CombinedOutput()
	if runCtx.Err() != nil {
		// A timeout in the acceptance command is a property of the solution —
		// an infinite loop is a failure — not a harness error.
		return false, string(output) + "\n[acceptance command timed out]", nil
	}
	return err == nil, string(output), nil
}

// snapshot records the content hash of every file, so the diff after a run is
// exact rather than inferred from timestamps.
func snapshot(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
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
		out[filepath.ToSlash(rel)] = h
		return nil
	})
	return out, err
}

func changedFiles(before, after map[string]string) []string {
	var out []string
	for path, hash := range after {
		if before[path] != hash {
			out = append(out, path)
		}
	}
	for path := range before {
		if _, still := after[path]; !still {
			out = append(out, path)
		}
	}
	return out
}

// intersect returns the changed paths matching any protected prefix.
func intersect(changed, protected []string) []string {
	if len(protected) == 0 {
		return nil
	}
	var out []string
	for _, c := range changed {
		for _, p := range protected {
			if c == p || strings.HasPrefix(c, strings.TrimSuffix(p, "/")+"/") {
				out = append(out, c)
				break
			}
		}
	}
	return out
}

func sanitise(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n… (output truncated)"
}
