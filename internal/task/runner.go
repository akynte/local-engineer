package task

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/akynte/local-engineer/internal/artifacts"
	"github.com/akynte/local-engineer/internal/broker"
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
	// Broker opens the human gates of §3.3. Nil means no gates: every
	// decision is automatic, which `le doctor` reports, because a system
	// running without gates should never be a surprise.
	Broker *broker.Broker
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
	// TokensUsed is the total the engine spent across every attempt.
	//
	// The per-attempt count is journalled, but a caller comparing the cost of
	// two configurations needs the sum, and a zero here reads as "this
	// pipeline used no tokens" rather than "nobody added them up".
	TokensUsed int `json:"tokens_used"`
	// Candidate is the worktree content hash the verdict describes.
	Candidate string `json:"candidate"`
	// OutOfScope lists files changed outside the declared scope.
	OutOfScope []string `json:"out_of_scope,omitempty"`
	// Diff is the change the task produced.
	Diff string `json:"diff,omitempty"`
	// Verified says which state the verdict describes. A verdict that does not
	// say what it examined is not usable evidence.
	Verified string `json:"verified"`
	// Gate is the human gate the task is waiting at, when it is waiting.
	Gate *broker.Gate `json:"gate,omitempty"`
	// Branch names where the change lives once the task is done with it. The
	// worktree is a checkout; the branch is the work.
	Branch string `json:"branch,omitempty"`
	// Worktree is the checkout path, kept while a task is unfinished or
	// waiting at a gate so a person can look at it.
	Worktree string `json:"worktree,omitempty"`
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

	// A step whose dependency has not been accepted would run against code
	// that does not exist yet, and its verification findings would be about
	// the wrong thing.
	blockers, err := r.Store.Blockers(ctx, taskID)
	if err != nil {
		return nil, err
	}
	if len(blockers) > 0 {
		names := make([]string, len(blockers))
		for i, b := range blockers {
			names[i] = fmt.Sprintf("%s (%s)", b.ID, b.State)
		}
		if err := r.Store.SetState(ctx, taskID, StateBlocked); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("task %s is blocked on %s", taskID, strings.Join(names, ", "))
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
		// The synced state becomes the baseline, so the task's diff is the
		// task's own work rather than the operator's uncommitted changes.
		if err := wt.Rebase(ctx); err != nil {
			return nil, fmt.Errorf("task %s: baseline the synced state: %w", t.ID, err)
		}
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
		out.Branch = wt.Branch
		out.Worktree = wt.Path
	}

	r.cleanup(ctx, &t, wt, out)
	return out, runErr
}

// cleanup decides what to keep after a run.
//
// The branch is ALWAYS kept when the task changed something. It is the only
// place the work exists: the worktree is a checkout, and deleting the branch
// with it destroys the change — including the one a pending gate is asking
// about. An earlier version did exactly that, so a gate would show a diff of
// something that no longer existed anywhere.
//
// The checkout directory is removed only when the task is finished and
// nobody needs to look at it: a failed task's checkout is worth far more than
// the disk it uses.
func (r *Runner) cleanup(ctx context.Context, t *Task, wt *worktree.Worktree, out *Outcome) {
	if out == nil {
		return
	}
	// Commit first, whatever the outcome. Until this runs the work exists only
	// in the checkout directory, so a gate would be asking about a change that
	// vanishes when the directory does, and a failed task would leave nothing
	// to inspect after cleanup.
	committed, err := wt.Commit(ctx, commitMessage(t, out))
	if err != nil {
		r.logf("task %s: committing the change to %s: %v", t.ID, wt.Branch, err)
	}
	if !committed {
		out.Branch = ""
	}
	// A task waiting at a gate keeps everything: the person deciding may want
	// to look at the checkout, not just the diff.
	if out.Gate != nil && out.Gate.Open() {
		r.logf("task %s: waiting at a gate; the checkout is at %s", t.ID, wt.Path)
		return
	}
	if !out.Accepted {
		r.logf("task %s: not accepted; the checkout is at %s (branch %s)", t.ID, wt.Path, wt.Branch)
		return
	}

	// keepBranch mirrors whether there is anything on it worth keeping.
	if err := r.Worktrees.Remove(ctx, wt, committed); err != nil {
		r.logf("task %s: removing worktree: %v", t.ID, err)
		return
	}
	out.Worktree = ""
	if committed {
		r.logf("task %s: accepted; the change is on branch %s", t.ID, wt.Branch)
	}
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

		used, err := r.step(ctx, t, wt, attempt, before, feedback)
		if err != nil {
			return nil, err
		}
		out.TokensUsed += used

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
			// A gate before the work is applied, carrying the diff and the
			// findings — the deterministic evidence, not a summary of it.
			gate, gateErr := r.gate(ctx, t, out)
			if gateErr != nil {
				return nil, gateErr
			}
			switch {
			case gate.Decision == broker.Rejected:
				out.Accepted = false
				out.Reasons = append(out.Reasons, "rejected at the human gate: "+gate.Note)
				return r.finish(ctx, t, wt, out, StateFailed)
			case gate.Open():
				out.Gate = &gate
				out.Reasons = append(out.Reasons,
					"waiting at a human gate: "+gate.Question)
				return r.finish(ctx, t, wt, out, StateReview)
			}
			return r.finish(ctx, t, wt, out, StateAccepted)
		}

		// A change outside the declared scope is a decision, not automatically
		// a failure: sometimes the right fix genuinely touches another file.
		if len(scope) > 0 && r.Broker != nil {
			gate, err := r.Broker.Ask(ctx, t.ID, broker.KindOutOfScope,
				"This task changed files outside its declared scope. Allow it?",
				broker.Evidence{
					Summary:    fmt.Sprintf("%d file(s) outside scope %v", len(scope), budget.Scope),
					OutOfScope: scope,
					Diff:       out.Diff,
				})
			if err != nil {
				return nil, err
			}
			if gate.Open() {
				out.Gate = &gate
				return r.finish(ctx, t, wt, out, StateReview)
			}
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
	attempt int, before string, feedback []recipe.Result) (int, error) {

	pkt, err := r.Retriever.Build(ctx, retrieval.Request{
		Query:       t.Title,
		ExpandDepth: 1,
	})
	if err != nil {
		return 0, fmt.Errorf("task %s: retrieval: %w", t.ID, err)
	}
	// A packet carrying a foreign slice is an isolation incident, not a
	// degraded result: refuse rather than proceed (§2.3).
	if len(pkt.Rejected) > 0 {
		return 0, fmt.Errorf("task %s: retrieval returned slices from another workspace: %s",
			t.ID, strings.Join(pkt.Rejected, "; "))
	}

	h, err := r.Ledger.Begin(ctx, t.ID, ledger.KindRecipeRun, map[string]any{
		"kind": "engine_step", "engine": r.Engine.Name(), "attempt": attempt,
		"objective": t.Title, "worktree": wt.ID, "packet_tokens": pkt.Tokens,
	}, before)
	if err != nil {
		return 0, err
	}

	// The engine's own verification tool runs in the same sandbox as the
	// supervisor's, so a model checking its work sees exactly what the
	// completion contract will see.
	if setter, ok := r.Engine.(interface{ SetRecipeRunner(*recipe.Runner) }); ok {
		setter.SetRecipeRunner(&recipe.Runner{
			Sandbox: r.Sandbox, Spec: r.specFor(wt), Store: r.Artifacts,
		})
	}

	resp, stepErr := r.Engine.Step(ctx, engine.Request{
		TaskID: t.ID, Objective: t.Title, Worktree: wt.Path,
		Packet: pkt, Feedback: feedback, Attempt: attempt,
		Budget: engine.Budget{MaxTokens: t.Budget.MaxTokens},
	})
	if stepErr != nil {
		// A recorded failure is definite: recovery does not need to inspect.
		if err := h.Fail(ctx, stepErr); err != nil {
			return 0, err
		}
		return 0, fmt.Errorf("task %s: engine step: %w", t.ID, stepErr)
	}

	after, err := wt.Candidate()
	if err != nil {
		return 0, err
	}
	changed, _ := wt.ChangedFiles(ctx)
	return resp.TokensUsed, h.Complete(ctx, map[string]any{
		"summary": resp.Summary, "claims_done": resp.ClaimsDone,
		"changed_files": changed, "tokens": resp.TokensUsed,
		// An attempt that produced nothing because the output budget ran out
		// looks exactly like one that produced nothing because the model had
		// nothing to say. The journal is where that difference has to survive:
		// without it the history shows an unproductive attempt and no reason,
		// and the operator tunes the wrong knob.
		"truncated": resp.Truncated,
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

// gate asks the broker whether a completed task may be applied, carrying the
// impact of what it changed.
//
// A task that changed nothing is not gated: there is nothing to apply, and
// asking anyway would train people to approve without reading — which is how a
// gate stops being a gate.
func (r *Runner) gate(ctx context.Context, t *Task, out *Outcome) (broker.Gate, error) {
	if r.Broker == nil {
		return broker.Gate{Decision: broker.Approved, Note: "no broker configured"}, nil
	}
	if strings.TrimSpace(out.Diff) == "" {
		return broker.Gate{Decision: broker.Approved, Note: "the task changed nothing; there is nothing to apply"}, nil
	}
	ev := broker.Evidence{
		Summary: fmt.Sprintf("%s: %d attempt(s), every required check passed", t.Title, out.Attempts),
		Diff:    out.Diff,
	}
	for _, res := range out.Results {
		ev.Findings = append(ev.Findings,
			fmt.Sprintf("%s: %s — %s", res.Recipe, res.Status, res.Summary.Headline))
	}
	return r.Broker.Ask(ctx, t.ID, broker.KindApply,
		"This task met the completion contract. Apply its change?", ev)
}

// commitMessage describes what the task did, so `git log` on the branch reads
// as a record rather than as noise.
func commitMessage(t *Task, out *Outcome) string {
	verdict := "not accepted"
	if out.Accepted {
		verdict = "accepted"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", t.Title)
	fmt.Fprintf(&b, "task: %s\nverification: %s (%s)\nattempts: %d\n",
		t.ID, t.Verification, verdict, out.Attempts)
	for _, res := range out.Results {
		fmt.Fprintf(&b, "  %s: %s — %s\n", res.Recipe, res.Status, res.Summary.Headline)
	}
	return b.String()
}

func nextAfter(state State) string {
	switch state {
	case StateAccepted:
		return "review the diff and merge the task branch"
	case StateFailed:
		return "read the verification findings and decide whether to re-plan or abandon"
	case StateBlocked:
		return "the budget was exhausted; raise it or reduce the task's scope"
	case StateReview:
		return "answer the open gate with `le gate approve` or `le gate reject`"
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
