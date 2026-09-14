package task_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/task"
	"github.com/akynte/local-engineer/internal/workspace"
)

// These run a real task against a real git repository with a real Go
// toolchain: the completion contract is about what actually happens when
// verification runs, and a mocked runner would prove nothing about it.

func gitRepo(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=T", "GIT_AUTHOR_EMAIL=t@e.com",
			"GIT_COMMITTER_NAME=T", "GIT_COMMITTER_EMAIL=t@e.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q", "-b", "main")
	run("add", "-A")
	run("commit", "-q", "-m", "initial")
	return dir
}

func newRunner(t *testing.T, eng engine.Engine) (*task.Runner, *store.Store) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })

	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/task/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	r, err := task.NewRunner(st, eng, sandbox.ContainerRunner{}, "test-instance")
	if err != nil {
		t.Fatal(err)
	}
	r.Logf = t.Logf
	// A real toolchain environment: the recipes shell out to `go`.
	r.SandboxSpec = sandbox.Spec{
		ReadOnly: []string{"/usr", "/bin", "/lib", "/lib64", "/etc"},
		TmpDir:   t.TempDir(),
		Env: recipe.GoEnv(
			filepath.Join(t.TempDir(), "gocache"),
			goModCache(t),
			t.TempDir()),
	}
	return r, st
}

// goModCache reuses the host module cache so the fixture does not need the
// network, which GOPROXY=off forbids anyway.
func goModCache(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		return filepath.Join(t.TempDir(), "gomodcache")
	}
	return strings.TrimSpace(string(out))
}

func requireGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the Go toolchain is not on PATH")
	}
}

const goodModule = `module example.test/task

go 1.26
`

// A task over code that builds, vets and tests cleanly is accepted.
func TestVerificationOnlyTaskAcceptsCleanCode(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod":    goodModule,
		"a.go":      "package a\n\n// Add returns the sum.\nfunc Add(x, y int) int { return x + y }\n",
		"a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatal(\"bad\")\n\t}\n}\n",
	})

	r, st := newRunner(t, engine.Verify{})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "verify the package", Verification: recipe.Standard,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatalf("expected acceptance, reasons: %v", out.Reasons)
	}
	if out.Task.State != task.StateAccepted {
		t.Errorf("state = %s", out.Task.State)
	}
	if out.Candidate == "" {
		t.Error("the outcome must name the candidate it describes")
	}
	// Every required kind must appear in the evidence.
	kinds := map[recipe.Kind]bool{}
	for _, res := range out.Results {
		kinds[res.Kind] = true
	}
	for _, want := range recipe.Required(recipe.Standard) {
		if !kinds[want] {
			t.Errorf("no %s result in the evidence", want)
		}
	}
}

// A failing test blocks acceptance, and the reason carries the finding.
func TestFailingTestsBlockAcceptance(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod":    goodModule,
		"a.go":      "package a\n\nfunc Add(x, y int) int { return x - y }\n",
		"a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(1, 2) != 3 {\n\t\tt.Fatalf(\"got %d\", Add(1, 2))\n\t}\n}\n",
	})

	r, st := newRunner(t, engine.Verify{})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "verify", Verification: recipe.Standard,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted {
		t.Fatal("a failing test must block acceptance")
	}
	if out.Task.State != task.StateFailed {
		t.Errorf("state = %s, want failed", out.Task.State)
	}
	if !containsSubstr(out.Reasons, "test failed") && !containsSubstr(out.Reasons, "failed") {
		t.Errorf("reasons must carry the finding: %v", out.Reasons)
	}
}

// Code that does not compile skips the rest rather than producing noise.
func TestBuildFailureShortCircuitsAndBlocks(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod": goodModule,
		"a.go":   "package a\n\nfunc Broken() int { return undefinedThing() }\n",
	})

	r, st := newRunner(t, engine.Verify{})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "verify", Verification: recipe.Standard,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted {
		t.Fatal("code that does not compile must not be accepted")
	}
	var buildFailed, testSkipped bool
	for _, res := range out.Results {
		if res.Kind == recipe.KindBuild && res.Status == recipe.Fail {
			buildFailed = true
		}
		if res.Kind == recipe.KindTest && res.Status == recipe.Skipped {
			testSkipped = true
		}
	}
	if !buildFailed {
		t.Error("the build recipe should have failed")
	}
	if !testSkipped {
		t.Error("tests should be skipped when the build fails, not run against uncompilable code")
	}
}

// The engine works in an isolated worktree; the operator's checkout is
// untouched even when the engine writes.
func TestTheTaskEditsAWorktreeNotTheRepository(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod": goodModule,
		"a.go":   "package a\n\nfunc Add(x, y int) int { return x + y }\n",
	})
	original, err := os.ReadFile(filepath.Join(repo, "a.go"))
	if err != nil {
		t.Fatal(err)
	}

	r, st := newRunner(t, &writingEngine{
		path: "a.go",
		body: "package a\n\n// Add returns the sum of x and y.\nfunc Add(x, y int) int { return x + y }\n",
	})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "document Add", Verification: recipe.Low,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatalf("expected acceptance: %v", out.Reasons)
	}
	// The operator's checkout must be byte-identical.
	after, err := os.ReadFile(filepath.Join(repo, "a.go"))
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(original) {
		t.Fatal("the task modified the operator's working copy instead of its own worktree")
	}
	// But the change is visible in the outcome's diff.
	if !strings.Contains(out.Diff, "returns the sum") {
		t.Errorf("the diff should carry the task's change:\n%s", out.Diff)
	}
}

// A change outside the declared scope blocks acceptance even when everything
// verifies.
func TestOutOfScopeChangeBlocksAcceptance(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod":       goodModule,
		"allowed/a.go": "package allowed\n\nfunc A() {}\n",
		"other/b.go":   "package other\n\nfunc B() {}\n",
	})

	r, st := newRunner(t, &writingEngine{
		path: "other/b.go",
		body: "package other\n\n// B does nothing, now with a comment.\nfunc B() {}\n",
	})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "touch only allowed/", Verification: recipe.Low,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute, Scope: []string{"allowed"}},
	}); err != nil {
		t.Fatal(err)
	}

	out, err := r.Run(ctx, id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted {
		t.Fatal("a change outside the declared scope must block acceptance")
	}
	if len(out.OutOfScope) == 0 || out.OutOfScope[0] != "other/b.go" {
		t.Errorf("the out-of-scope file must be named, got %v", out.OutOfScope)
	}
}

// Every attempt is journalled intent-first, so an interruption is reconcilable.
func TestEveryAttemptIsJournalled(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod": goodModule,
		"a.go":   "package a\n\nfunc A() {}\n",
	})

	r, st := newRunner(t, engine.Verify{})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "verify", Verification: recipe.Low,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, id, repo); err != nil {
		t.Fatal(err)
	}

	ops, err := ledger.New(st).Operations(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) < 2 {
		t.Fatalf("expected an engine step and a verification operation, got %d", len(ops))
	}
	for _, op := range ops {
		if op.Uncertain() {
			t.Errorf("operation %d (%s) was left uncertain by a completed run", op.Seq, op.Kind)
		}
		if len(op.Intent) == 0 {
			t.Errorf("operation %d has no intent", op.Seq)
		}
	}
	// A checkpoint must exist so a restart resumes from a decided state.
	seq, state, _, _, err := ledger.New(st).LastCheckpoint(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if state == "" || seq == 0 {
		t.Errorf("expected a checkpoint, got seq=%d state=%q", seq, state)
	}
}

// Two instances must never run the same task's worktree.
func TestASecondRunnerCannotTakeALiveLease(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{"go.mod": goodModule, "a.go": "package a\n"})

	r, st := newRunner(t, engine.Verify{})
	ctx := context.Background()

	id := task.NewID("t")
	if err := task.NewStore(st).Create(ctx, task.Task{
		ID: id, Title: "verify", Verification: recipe.Low,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: time.Minute},
	}); err != nil {
		t.Fatal(err)
	}

	// Another instance already holds the lease on this task's worktree.
	if _, err := ledger.New(st).AcquireLease(ctx, "wt-"+id, id, "other-instance", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Run(ctx, id, repo); err == nil {
		t.Fatal("a second runner must not proceed against a live lease")
	}
	_ = r
}

// writingEngine is a test engine that writes a fixed file, standing in for
// whatever produces edits.
type writingEngine struct {
	path string
	body string
}

func (e *writingEngine) Name() string                 { return "writing-test-engine" }
func (e *writingEngine) Health(context.Context) error { return nil }
func (e *writingEngine) Close() error                 { return nil }

func (e *writingEngine) Step(_ context.Context, req engine.Request) (*engine.Response, error) {
	p := filepath.Join(req.Worktree, filepath.FromSlash(e.path))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(p, []byte(e.body), 0o644); err != nil {
		return nil, err
	}
	return &engine.Response{Summary: "wrote " + e.path, ClaimsDone: true}, nil
}

// A worktree is created from a commit, so without an explicit sync a
// verification describes the last commit rather than what the operator is
// looking at. A pass that describes code nobody is running is the worst
// failure this system can produce, so it gets its own test.
func TestUncommittedChangesAreVerifiedWhenSyncIsOn(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod":    goodModule,
		"a.go":      "package a\n\nfunc Add(x, y int) int { return x + y }\n",
		"a_test.go": "package a\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif Add(2, 3) != 5 {\n\t\tt.Fatalf(\"got %d\", Add(2, 3))\n\t}\n}\n",
	})
	// Break it without committing: exactly the state a developer is in when
	// they ask whether their change is good.
	if err := os.WriteFile(filepath.Join(repo, "a.go"),
		[]byte("package a\n\nfunc Add(x, y int) int { return x - y }\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	run := func(sync bool) *task.Outcome {
		t.Helper()
		r, st := newRunner(t, engine.Verify{})
		r.SyncUncommitted = sync
		id := task.NewID("t")
		if err := task.NewStore(st).Create(context.Background(), task.Task{
			ID: id, Title: "verify", Verification: recipe.Standard,
			Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
		}); err != nil {
			t.Fatal(err)
		}
		out, err := r.Run(context.Background(), id, repo)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}

	withSync := run(true)
	if withSync.Accepted {
		t.Fatal("with sync on, the broken uncommitted change must block acceptance")
	}
	if !strings.Contains(withSync.Verified, "uncommitted") {
		t.Errorf("the outcome must say it examined the uncommitted state, got %q", withSync.Verified)
	}

	// Without sync the committed code is verified, which passes — and the
	// outcome must say so, or the two verdicts are indistinguishable.
	withoutSync := run(false)
	if !withoutSync.Accepted {
		t.Fatalf("the committed state is good and should be accepted: %v", withoutSync.Reasons)
	}
	if !strings.Contains(withoutSync.Verified, "committed") {
		t.Errorf("the outcome must say it examined the committed state, got %q", withoutSync.Verified)
	}
}

// Untracked files are in no diff, but they are part of what the operator sees.
func TestSyncCarriesUntrackedFiles(t *testing.T) {
	requireGo(t)
	repo := gitRepo(t, map[string]string{
		"go.mod": goodModule,
		"a.go":   "package a\n\nfunc Add(x, y int) int { return x + y }\n",
	})
	// A new, never-committed test that fails.
	if err := os.WriteFile(filepath.Join(repo, "new_test.go"),
		[]byte("package a\n\nimport \"testing\"\n\nfunc TestNew(t *testing.T) { t.Fatal(\"deliberate\") }\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	r, st := newRunner(t, engine.Verify{})
	r.SyncUncommitted = true
	id := task.NewID("t")
	if err := task.NewStore(st).Create(context.Background(), task.Task{
		ID: id, Title: "verify", Verification: recipe.Standard,
		Budget: task.Budget{MaxAttempts: 1, MaxWallTime: 5 * time.Minute},
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.Run(context.Background(), id, repo)
	if err != nil {
		t.Fatal(err)
	}
	if out.Accepted {
		t.Fatal("an untracked failing test must be seen and must block acceptance")
	}
	if !strings.Contains(out.Verified, "untracked") {
		t.Errorf("the outcome should mention the untracked files it pulled in, got %q", out.Verified)
	}
	// The source repository must be untouched by the sync.
	if _, err := os.Stat(filepath.Join(repo, "new_test.go")); err != nil {
		t.Errorf("the source repository was disturbed: %v", err)
	}
}
