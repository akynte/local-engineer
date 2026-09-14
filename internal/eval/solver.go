package eval

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/engine/native"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/llm"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/task"
	"github.com/akynte/local-engineer/internal/worktree"
)

// SystemSolver runs a task through this project's own pipeline, configured by
// the arm.
//
// Each arm's flags turn a piece of the system off, and they must turn it off
// *properly*. An ablation that leaves the ablated component partly running
// measures nothing, so the differences are structural — a different retriever,
// a different recipe set — rather than a flag the pipeline might ignore.
type SystemSolver struct {
	Store   *store.Store
	Router  *llm.Router
	Sandbox sandbox.Runner
	// SandboxSpec is the base spec; the task copy is added per run.
	SandboxSpec sandbox.Spec
	// Profile-derived limits (§9.3: never hardcoded).
	MaxTools    int
	MaxTokens   int
	Temperature float64
	Thinking    string
	Logf        func(format string, args ...any)
}

func (s *SystemSolver) logf(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

// Solve runs one attempt under one arm.
func (s *SystemSolver) Solve(ctx context.Context, req SolveRequest) (SolveResult, error) {
	provider, err := s.Router.For(llm.Role(roleOf(req.Arm)))
	if err != nil {
		return SolveResult{}, err
	}

	// The task copy is not a git repository, and the pipeline's worktree
	// machinery needs one. Initialising here keeps the fixture on disk free of
	// version control, so a fixture cannot smuggle history to the model.
	if err := initRepo(ctx, req.Worktree); err != nil {
		return SolveResult{}, fmt.Errorf("preparing the task repository: %w", err)
	}

	eng, err := s.engineFor(req.Arm, provider)
	if err != nil {
		return SolveResult{}, err
	}
	defer eng.Close()

	if !req.Arm.Supervised {
		return s.solveUnsupervised(ctx, req, eng)
	}
	return s.solveSupervised(ctx, req, eng)
}

// solveUnsupervised gives the model the objective and the worktree and nothing
// else: no retrieval, no graph, no verification loop.
//
// This is the comparison that says whether the harness earns its complexity,
// so it must be a fair version of the baseline — the same model, the same
// editing tools, the same budget. What it does not get is everything this
// project adds.
func (s *SystemSolver) solveUnsupervised(ctx context.Context, req SolveRequest, eng engine.Engine) (SolveResult, error) {
	var total int
	for attempt := 1; attempt <= req.Task.Budget.MaxAttempts; attempt++ {
		// A budget that runs out is an outcome: the task was attempted and not
		// solved. A user interrupt is not — recording a verdict for a run the
		// operator stopped would put a fabricated data point in the results.
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.Canceled) {
				return SolveResult{Attempts: attempt - 1, Tokens: total}, err
			}
			return SolveResult{Attempts: attempt - 1, Tokens: total,
				Reasons: []string{"the wall-clock budget was exhausted"}}, nil
		}
		resp, err := eng.Step(ctx, engine.Request{
			TaskID: req.Task.ID, Objective: req.Task.Objective,
			Worktree: req.Worktree, Attempt: attempt,
			// No packet: retrieval is what the supervised arm adds.
		})
		if err != nil {
			return SolveResult{Attempts: attempt, Tokens: total}, err
		}
		total += resp.TokensUsed
		if resp.ClaimsDone {
			// With no verification there is nothing to check the claim
			// against, which is the point of the baseline.
			return SolveResult{Claimed: true, Attempts: attempt, Tokens: total,
				Reasons: []string{"the model reported completion; no verification was run"}}, nil
		}
	}
	return SolveResult{Attempts: req.Task.Budget.MaxAttempts, Tokens: total,
		Reasons: []string{"the attempt budget was exhausted"}}, nil
}

// solveSupervised runs the full pipeline, minus whatever the arm ablates.
func (s *SystemSolver) solveSupervised(ctx context.Context, req SolveRequest, eng engine.Engine) (SolveResult, error) {
	runner, err := task.NewRunner(s.Store, eng, s.Sandbox, "eval")
	if err != nil {
		return SolveResult{}, err
	}
	runner.Logf = s.logf
	runner.SandboxSpec = s.SandboxSpec
	// No broker: a gate would block an unattended run, and the completion
	// contract is what is being measured, not the approval policy.
	runner.Broker = nil

	level := recipe.Level(req.Task.Verification)
	if _, ok := recipe.ParseLevel(string(level)); !ok {
		level = recipe.Standard
	}
	if !req.Arm.Verification {
		// The ablation: the lowest level still compiles the result, so the
		// arm is "no test feedback", not "no checks at all". Removing the
		// build too would measure a different thing.
		level = recipe.Low
	}

	id := task.NewID("eval")
	if err := task.NewStore(s.Store).Create(ctx, task.Task{
		ID: id, Title: req.Task.Objective, Verification: level,
		Budget: task.Budget{
			MaxAttempts: req.Task.Budget.MaxAttempts,
			MaxWallTime: time.Until(req.Deadline),
			MaxTokens:   req.Task.Budget.MaxTokens,
			Scope:       req.Task.Scope,
		},
	}); err != nil {
		return SolveResult{}, err
	}

	out, err := runner.Run(ctx, id, req.Worktree)
	if err != nil {
		return SolveResult{}, err
	}

	// The pipeline works in its own worktree; the harness judges the copy it
	// prepared. Bringing the accepted change back is what makes the two the
	// same thing.
	if out.Accepted && out.Branch != "" {
		if err := applyBranch(ctx, req.Worktree, out.Branch); err != nil {
			return SolveResult{}, fmt.Errorf("applying the accepted change: %w", err)
		}
	}
	return SolveResult{
		Claimed: out.Accepted, Attempts: out.Attempts, Reasons: out.Reasons,
	}, nil
}

// engineFor builds the editing engine, with retrieval wired according to the
// arm.
func (s *SystemSolver) engineFor(arm Arm, provider llm.Provider) (engine.Engine, error) {
	opts := native.Options{
		Provider: provider, Logf: s.Logf,
		MaxTools: s.MaxTools, MaxTokens: s.MaxTokens,
		Temperature: s.Temperature, Thinking: s.Thinking,
	}
	if arm.Supervised {
		opts.Retriever = retrieval.New(s.Store)
		if arm.Graph {
			// The graph is what the ablation removes: with it off the engine
			// keeps lexical search and loses symbol lookup, graph expansion
			// and impact analysis.
			opts.Graph = graph.New(s.Store)
		}
	}
	return native.New(opts)
}

func roleOf(arm Arm) string {
	if arm.Role == "" {
		return string(llm.RoleCoding)
	}
	return arm.Role
}

// initRepo makes the task copy a git repository with one commit, so the
// pipeline's worktree machinery has a base.
func initRepo(ctx context.Context, dir string) error {
	if worktree.IsRepository(ctx, dir) {
		return nil
	}
	steps := [][]string{
		{"init", "-q", "-b", "main"},
		{"add", "-A"},
		{"-c", "user.name=eval", "-c", "user.email=eval@localhost",
			"commit", "-q", "-m", "evaluation fixture"},
	}
	for _, args := range steps {
		//nolint:gosec // fixed arguments; dir is passed via -C
		cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(stderr.String()))
		}
	}
	return nil
}

// applyBranch fast-forwards the task copy onto the branch the pipeline
// produced, so the harness judges what the system actually built.
func applyBranch(ctx context.Context, dir, branch string) error {
	//nolint:gosec // a branch name this process generated
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "checkout", "-q", branch, "--", ".")
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
	}
	return nil
}
