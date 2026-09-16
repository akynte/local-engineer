package task_test

import (
	"context"
	"errors"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

// fakeFreshener records what the runner asked of it.
type fakeFreshener struct {
	dirty     int
	dirtyErr  error
	refreshed []string
	refreshEr error
}

func (f *fakeFreshener) Dirty(context.Context) (int, error) { return f.dirty, f.dirtyErr }
func (f *fakeFreshener) Refresh(_ context.Context, root string) error {
	f.refreshed = append(f.refreshed, root)
	return f.refreshEr
}

func runWithFreshener(t *testing.T, f *fakeFreshener) (*fakeFreshener, error) {
	t.Helper()
	r, st := newRunner(t, engine.Verify{})
	r.Freshener = f
	store := task.NewStore(st)
	tk := task.Task{ID: task.NewID("t"), Title: "x", Verification: recipe.Low,
		Budget: task.Budget{MaxAttempts: 1}}
	if err := store.Create(context.Background(), tk); err != nil {
		t.Fatal(err)
	}
	repo := gitRepo(t, map[string]string{"go.mod": "module example.com/demo\n\ngo 1.26\n", "a.go": "package demo\n"})
	_, err := r.Run(context.Background(), tk.ID, repo)
	return f, err
}

// §3.4 puts the re-analysis before a step that needs the graph. Nothing
// performed it: the watcher marked scopes dirty, `le doctor` said in its fix
// text that dirty units "are re-analysed before any step that needs the
// graph", and the only reader of that mark was the doctor check itself.
func TestADirtyIndexIsRefreshedBeforeAStep(t *testing.T) {
	f, err := runWithFreshener(t, &fakeFreshener{dirty: 3})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.refreshed) == 0 {
		t.Fatal("a dirty index was never refreshed; the step used a stale graph")
	}
	// The graph describes the repository the index was built from, not the
	// task's worktree — refreshing the worktree would re-index a checkout
	// nobody queries.
	for _, root := range f.refreshed {
		if root == "" {
			t.Error("refresh was called with no repository root")
		}
	}
}

// A clean index must not trigger a re-analysis: this runs before every step,
// and paying for it when nothing changed would tax every task.
func TestACleanIndexIsNotRefreshed(t *testing.T) {
	f, err := runWithFreshener(t, &fakeFreshener{dirty: 0})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(f.refreshed) != 0 {
		t.Errorf("a clean index was refreshed %d time(s)", len(f.refreshed))
	}
}

// A refresh that cannot run leaves a degraded graph; refusing the task outright
// is worse, and the operator has `le doctor` and `le index` for the case where
// re-indexing genuinely cannot happen.
func TestARefreshFailureDoesNotFailTheTask(t *testing.T) {
	_, err := runWithFreshener(t, &fakeFreshener{dirty: 1, refreshEr: errors.New("disk is full")})
	if err != nil {
		t.Errorf("a failed refresh took the task down: %v", err)
	}
}

// Likewise an unreadable dirty count.
func TestAnUnreadableDirtyCountDoesNotFailTheTask(t *testing.T) {
	f, err := runWithFreshener(t, &fakeFreshener{dirtyErr: errors.New("index is locked")})
	if err != nil {
		t.Errorf("an unreadable dirty count took the task down: %v", err)
	}
	if len(f.refreshed) != 0 {
		t.Error("a refresh ran despite the dirty count being unreadable")
	}
}
