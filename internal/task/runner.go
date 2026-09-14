package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/artifacts"
	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/worktree"
)

// Runner executes one task to a terminal state.
//
// Every side effect it performs is journalled intent-first, so an interruption
// at any point leaves a state recovery can reconcile rather than guess at
// (§7.1).
type Runner struct {
	Store     *Store
	Ledger    *ledger.Ledger
	Worktrees *worktree.Manager
	Retriever *retrieval.Retriever
	Artifacts *artifacts.Store
	Engine    engine.Engine
	Sandbox   sandbox.Runner

	// SandboxSpec is the base specification; the worktree is added per task.
	SandboxSpec sandbox.Spec
	// SyncUncommitted copies the repository's uncommitted state into the task
	// worktree before running. It is what `le task verify` wants: verifying
	// the last commit while the operator looks at uncommitted changes would
	// produce a pass describing code nobody is running.
	//
	// It is off for engine-driven tasks, which should start from a clean
	// committed base.
	SyncUncommitted bool
	// Holder identifies this supervisor instance in worktree leases.
	Holder string
	// Logf reports progress. Nil discards it.
	Logf func(format string, args ...any)

	// store and dirs let the runner ask for directories rather than creating
	// them: §2.3 confines that to internal/store.
	store *store.Store
	dirs  store.TaskDirs
}

// NewRunner wires a runner from a workspace store.
func NewRunner(s *store.Store, eng engine.Engine, sb sandbox.Runner, holder string) (*Runner, error) {
	dirs, err := s.TaskDirs()
	if err != nil {
		return nil, err
	}
	wm, err := worktree.NewManager(dirs.Worktrees)
	if err != nil {
		return nil, err
	}
	return &Runner{
		Store: NewStore(s), Ledger: ledger.New(s), Worktrees: wm,
		Retriever: retrieval.New(s), Artifacts: artifacts.New(s),
		Engine: eng, Sandbox: sb, Holder: holder,
		store: s, dirs: dirs,
	}, nil
}

func (r *Runner) logf(format string, args ...any) {
	if r.Logf != nil {
		r.Logf(format, args...)
	}
}

// Outcome is the result of a run.
type Outcome struct {
	Task Task `json:"task"`
	// Accepted reports whether the completion contract was satisfied.
	Accepted bool `json:"accepted"`
	// Reasons explains the verdict. Populated on acceptance too: "why did this
	// pass" must be as answerable as "why did this fail".
	Reasons []string `json:"reasons"`
	// Results are the verification results from the final attempt.
	Results []recipe.Result `json:"results"`
	// Attempts is how many engine steps were taken.
	Attempts int `json:"attempts"`
	// Candidate is the worktree content hash the verdict describes.
	Candidate string `json:"candidate"`
	// OutOfScope lists files changed outside the declared scope.
	OutOfScope []string `json:"out_of_scope,omitempty"`
	// Diff is the change the task produced.
	Diff string `json:"diff,omitempty"`
	// Verified says which state the verdict describes. A verdict that does not
	// say what it examined is not usable evidence.
	Verified string `json:"verified"`
}

// Run executes a task against a repository.
func (r *Runner) Run(ctx context.Context, taskID, repoPath string) (*Outcome, error) {
	t, err := r.Store.Get(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if t.State.Terminal() {
		return nil, fmt.Errorf("task %s is already %s", t.ID, t.State)
	}
	if r.Engine == nil {
		return nil, engine.ErrNoEngine
	}

	wt, err := r.Worktrees.Create(ctx, repoPath, t.ID)
	if err != nil {
		// A pre-existing worktree means a previous run of this task did not
		// clean up; recovery, not a fresh run, is the right response.
		if existing, openErr := r.Worktrees.Open(ctx, repoPath, t.ID); openErr == nil {
			wt = existing
		} else {
			return nil, err
		}
	}

	// The lease is what guarantees two instances never write the same
	// worktree (§7.2 step 4).
	lease, err := r.Ledger.AcquireLease(ctx, wt.ID, t.ID, r.Holder, 0)
	if err != nil {
		return nil, fmt.Errorf("task %s: %w", t.ID, err)
	}
	defer func() { _ = r.Ledger.ReleaseLease(ctx, wt.ID, r.Holder) }()
	_ = lease

	verified := "the committed state (HEAD)"
	if r.SyncUncommitted {
		rep, syncErr := wt.SyncFrom(ctx, repoPath)
		if syncErr != nil {
			return nil, fmt.Errorf("task %s: %w", t.ID, syncErr)
		}
		verified = rep.Describe()
		r.logf("task %s: %s", t.ID, verified)
	}

	if err := r.Store.SetWorktree(ctx, t.ID, wt.ID); err != nil {
		return nil, err
	}
	if err := r.Store.SetState(ctx, t.ID, StateRunning); err != nil {
		return nil, err
	}

	out, runErr := r.run(ctx, &t, wt)
	if out != nil {
		out.Verified = verified
	}

	// The worktree is kept when the task did not succeed: a checkout that
	// shows what went wrong is worth more than the disk it uses.
	if out != nil && out.Accepted {
		if err := r.Worktrees.Remove(ctx, wt, false); err != nil {
			r.logf("task %s: removing worktree: %v", t.ID, err)
		}
	}
	return out, runErr
}

func (r *Runner) run(ctx context.Context, t *Task, wt *worktree.Worktree) (*Outcome, error) {
	budget := t.Budget
	if budget.MaxAttempts <= 0 {
		budget = DefaultBudget()
	}
	if budget.MaxWallTime > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget.MaxWallTime)
		defer cancel()
	}

	out := &Outcome{Task: *t}
	var feedback []recipe.Result

	for attempt := 1; attempt <= budget.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			out.Reasons = append(out.Reasons, "budget exhausted: "+err.Error())
			return r.finish(ctx, t, wt, out, StateBlocked)
		}
		out.Attempts = attempt

		before, err := wt.Candidate()
		if err != nil {
			return nil, err
		}

		if err := r.step(ctx, t, wt, attempt, before, feedback); err != nil {
			return nil, err
		}

		after, err := wt.Candidate()
		if err != nil {
			return nil, err
		}
		out.Candidate = after

		results, err := r.verify(ctx, t, wt, after)
		if err != nil {
			return nil, err
		}
		out.Results = results
		feedback = failedOnly(results)

		scope, err := wt.OutOfScope(ctx, budget.Scope)
		if err != nil {
			return nil, err
		}
		out.OutOfScope = scope

		accepted, reasons := Accept(t.Verification, results, after, scope)
		out.Reasons = reasons
		if accepted {
			out.Accepted = true
			if diff, err := wt.Diff(ctx); err == nil {
				out.Diff = diff
			}
			return r.finish(ctx, t, wt, out, StateAccepted)
		}
		r.logf("task %s attempt %d/%d not accepted: %s",
			t.ID, attempt, budget.MaxAttempts, strings.Join(reasons, "; "))
	}

	if diff, err := wt.Diff(ctx); err == nil {
		out.Diff = diff
	}
	out.Reasons = append(out.Reasons,
		fmt.Sprintf("the attempt budget of %d was exhausted without meeting the completion contract",
			budget.MaxAttempts))
	return r.finish(ctx, t, wt, out, StateFailed)
}

// step runs one engine attempt, journalled intent-first.
func (r *Runner) step(ctx context.Context, t *Task, wt *worktree.Worktree,
	attempt int, before string, feedback []recipe.Result) error {

	pkt, err := r.Retriever.Build(ctx, retrieval.Request{
		Query:       t.Title,
		ExpandDepth: 1,
	})
	if err != nil {
		return fmt.Errorf("task %s: retrieval: %w", t.ID, err)
	}
	// A packet carrying a foreign slice is an isolation incident, not a
	// degraded result: refuse rather than proceed (§2.3).
	if len(pkt.Rejected) > 0 {
		return fmt.Errorf("task %s: retrieval returned slices from another workspace: %s",
			t.ID, strings.Join(pkt.Rejected, "; "))
	}

	h, err := r.Ledger.Begin(ctx, t.ID, ledger.KindRecipeRun, map[string]any{
		"kind": "engine_step", "engine": r.Engine.Name(), "attempt": attempt,
		"objective": t.Title, "worktree": wt.ID, "packet_tokens": pkt.Tokens,
	}, before)
	if err != nil {
		return err
	}

	resp, stepErr := r.Engine.Step(ctx, engine.Request{
		TaskID: t.ID, Objective: t.Title, Worktree: wt.Path,
		Packet: pkt, Feedback: feedback, Attempt: attempt,
		Budget: engine.Budget{MaxTokens: t.Budget.MaxTokens},
	})
	if stepErr != nil {
		// A recorded failure is definite: recovery does not need to inspect.
		if err := h.Fail(ctx, stepErr); err != nil {
			return err
		}
		return fmt.Errorf("task %s: engine step: %w", t.ID, stepErr)
	}

	after, err := wt.Candidate()
	if err != nil {
		return err
	}
	changed, _ := wt.ChangedFiles(ctx)
	return h.Complete(ctx, map[string]any{
		"summary": resp.Summary, "claims_done": resp.ClaimsDone,
		"changed_files": changed, "tokens": resp.TokensUsed,
	}, after, "")
}

// verify runs the recipes the task's level demands and records each result as
// evidence against the candidate it describes.
func (r *Runner) verify(ctx context.Context, t *Task, wt *worktree.Worktree, candidate string) ([]recipe.Result, error) {
	runner := &recipe.Runner{
		Sandbox: r.Sandbox,
		Spec:    r.specFor(wt),
		Store:   r.Artifacts,
	}
	recipes := recipe.GoRecipes(t.Verification)
	if len(recipes) == 0 {
		return nil, nil
	}

	h, err := r.Ledger.Begin(ctx, t.ID, ledger.KindRecipeRun, map[string]any{
		"kind": "verification", "level": string(t.Verification), "recipes": names(recipes),
	}, candidate)
	if err != nil {
		return nil, err
	}

	results := runner.RunAll(ctx, recipes, wt.Path, candidate)

	for i, res := range results {
		evidenceID := fmt.Sprintf("%s-%s-%d", t.ID, res.Kind, i)
		if err := r.Ledger.RecordEvidence(ctx, evidenceID, t.ID, string(res.Kind),
			string(res.Status), res.Candidate, res.ArtifactHash, res.Summary.Headline, res.Summary); err != nil {
			return nil, err
		}
	}
	if err := h.Complete(ctx, map[string]any{"results": summaries(results)}, candidate, ""); err != nil {
		return nil, err
	}
	return results, nil
}

// specFor builds the sandbox specification for a task: its worktree writable,
// the toolchain and its caches reachable, and nothing else.
//
// The writable set is derived from the environment the recipes will run with,
// so a cache directory named in GOCACHE is always one the sandbox granted. The
// alternative — expecting every caller to keep the two in step — produces a
// permission error from inside the compiler, which reads as a broken sandbox
// rather than a missing grant.
func (r *Runner) specFor(wt *worktree.Worktree) sandbox.Spec {
	spec := r.SandboxSpec

	tmp := spec.TmpDir
	if tmp == "" {
		tmp = r.dirs.Tmp
		spec.TmpDir = tmp
	}
	if len(spec.Env) == 0 {
		spec.Env = recipe.GoEnv(r.dirs.GoBuildCache, r.dirs.GoModCache, tmp)
	}

	rw, ro := recipe.GoSandboxPaths(envValue(spec.Env, "GOCACHE"), envValue(spec.Env, "GOMODCACHE"), tmp)
	spec.ReadWrite = dedupe(append(append([]string{wt.Path}, spec.ReadWrite...), rw...))
	spec.ReadOnly = dedupe(append(append([]string{}, spec.ReadOnly...), ro...))
	// Device nodes every ordinary program expects. Granting them read-write is
	// correct: /dev/null is written to constantly.
	spec.ReadWrite = append(spec.ReadWrite, recipe.DeviceFiles()...)
	spec.Dir = wt.Path

	// Each directory must exist before the sandbox can grant it: Landlock
	// rules on a missing path are dropped, and the task then fails on a write
	// the sandbox intended to allow. The store owns creation (§2.3).
	if r.store != nil {
		if err := r.store.EnsureSandboxDirs(spec.ReadWrite); err != nil {
			r.logf("sandbox: %v", err)
		}
	}
	return spec
}

// envValue reads a variable out of an environment slice.
func envValue(env []string, key string) string {
	prefix := key + "="
	for i := len(env) - 1; i >= 0; i-- {
		if strings.HasPrefix(env[i], prefix) {
			return strings.TrimPrefix(env[i], prefix)
		}
	}
	return ""
}

func dedupe(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := in[:0]
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (r *Runner) finish(ctx context.Context, t *Task, wt *worktree.Worktree,
	out *Outcome, state State) (*Outcome, error) {

	// A checkpoint so a restart resumes from a decided state rather than
	// re-deriving it (§7.2 step 2).
	if err := r.Ledger.Checkpoint(ctx, t.ID, string(state), map[string]any{
		"objective":      t.Title,
		"next_action":    nextAfter(state),
		"remaining_plan": []string{},
		"accepted":       out.Accepted,
		"reasons":        out.Reasons,
	}, out.Candidate); err != nil {
		return out, err
	}
	if err := r.Store.SetState(ctx, t.ID, state); err != nil {
		return out, err
	}
	out.Task.State = state
	return out, nil
}

func nextAfter(state State) string {
	switch state {
	case StateAccepted:
		return "review the diff and merge the task branch"
	case StateFailed:
		return "read the verification findings and decide whether to re-plan or abandon"
	case StateBlocked:
		return "the budget was exhausted; raise it or reduce the task's scope"
	}
	return "resume the task"
}

func failedOnly(results []recipe.Result) []recipe.Result {
	var out []recipe.Result
	for _, r := range results {
		if r.Status == recipe.Fail || r.Status == recipe.Error {
			out = append(out, r)
		}
	}
	return out
}

func names(recipes []recipe.Recipe) []string {
	out := make([]string, len(recipes))
	for i, r := range recipes {
		out[i] = r.Name
	}
	return out
}

func summaries(results []recipe.Result) []map[string]any {
	out := make([]map[string]any, len(results))
	for i, r := range results {
		out[i] = map[string]any{
			"recipe": r.Recipe, "kind": string(r.Kind), "status": string(r.Status),
			"headline": r.Summary.Headline, "artifact": r.ArtifactHash,
		}
	}
	return out
}

// ErrNotAccepted is returned when a caller asked for acceptance and the
// contract was not met.
var ErrNotAccepted = errors.New("task: completion contract not satisfied")

// Accept decides whether the completion contract is satisfied.
//
// This is the function the whole design points at, so its rules are explicit:
//
//  1. every recipe kind the level requires must have a result,
//  2. that result must be a pass — a skip or an error satisfies nothing,
//  3. it must have been produced against the current candidate, so evidence
//     for an older state of the code cannot be reused (§7.2),
//  4. no file may be changed outside the task's declared scope.
//
// No part of it consults what the engine claimed.
func Accept(level recipe.Level, results []recipe.Result, candidate string, outOfScope []string) (bool, []string) {
	byKind := map[recipe.Kind]recipe.Result{}
	for _, r := range results {
		// Keep the worst result per kind: one passing package does not excuse
		// a failing one.
		if prev, seen := byKind[r.Kind]; !seen || worse(r.Status, prev.Status) {
			byKind[r.Kind] = r
		}
	}

	var reasons []string
	ok := true

	for _, kind := range recipe.Required(level) {
		res, ran := byKind[kind]
		switch {
		case !ran:
			ok = false
			reasons = append(reasons, fmt.Sprintf("%s verification requires %s, which did not run", level, kind))
		case res.Status == recipe.Skipped:
			ok = false
			reasons = append(reasons, fmt.Sprintf("%s was skipped: %s", kind, res.Summary.Headline))
		case res.Status == recipe.Error:
			ok = false
			reasons = append(reasons, fmt.Sprintf("%s could not run: %s", kind, res.Err))
		case res.Status == recipe.Fail:
			ok = false
			reasons = append(reasons, fmt.Sprintf("%s failed: %s", kind, res.Summary.Headline))
		case candidate != "" && res.Candidate != "" && res.Candidate != candidate:
			ok = false
			reasons = append(reasons, fmt.Sprintf(
				"%s passed, but against an older candidate (%s); the worktree has changed since",
				kind, shortHash(res.Candidate)))
		default:
			reasons = append(reasons, fmt.Sprintf("%s passed: %s", kind, res.Summary.Headline))
		}
	}

	if len(outOfScope) > 0 {
		ok = false
		sort.Strings(outOfScope)
		shown := outOfScope
		if len(shown) > 5 {
			shown = shown[:5]
		}
		reasons = append(reasons, fmt.Sprintf(
			"%d file(s) changed outside the task's declared scope: %s",
			len(outOfScope), strings.Join(shown, ", ")))
	}
	return ok, reasons
}

// worse reports whether a is a worse outcome than b.
func worse(a, b recipe.Status) bool {
	rank := map[recipe.Status]int{recipe.Pass: 0, recipe.Skipped: 1, recipe.Error: 2, recipe.Fail: 3}
	return rank[a] > rank[b]
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// Elapsed is a helper for reporting.
func Elapsed(start time.Time) string { return time.Since(start).Round(time.Millisecond).String() }
