package task_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/task"
)

func failedTask(t *testing.T) (*task.Store, task.Task) {
	t.Helper()
	_, st := newRunner(t, engine.Verify{})
	store := task.NewStore(st)
	tk := task.Task{
		ID: task.NewID("t"), Title: "a task the machine could not run",
		Verification: recipe.Standard,
		Budget:       task.Budget{MaxAttempts: 3},
	}
	if err := store.Create(context.Background(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetState(context.Background(), tk.ID, task.StateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}
	return store, tk
}

// Three harness misconfigurations in one session — an output budget too small
// for the model's reasoning, a request longer than the provider's timeout, and
// memory pressure — each produced a failed task whose work was never really
// attempted. Fixing the cause did not help: the task refused to run, so its id
// and its journal were abandoned and the description was retyped as a new one.
func TestAFailedTaskCanBeRetried(t *testing.T) {
	store, tk := failedTask(t)

	got, err := store.Reopen(context.Background(), tk.ID)
	if err != nil {
		t.Fatalf("a failed task must be retryable: %v", err)
	}
	if got.State != task.StatePending {
		t.Errorf("state is %s, want pending", got.State)
	}

	// And the change must be durable, not just in the returned copy.
	reread, err := store.Get(context.Background(), tk.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if reread.State != task.StatePending {
		t.Errorf("the stored state is %s, want pending", reread.State)
	}
	// The identity is the point: retyping the task loses the record of what
	// was already tried.
	if reread.ID != tk.ID || reread.Title != tk.Title {
		t.Error("retry must keep the task's id and title")
	}
}

// An accepted task has been through the completion contract and its change may
// already be merged. Running it again would redo approved work against
// evidence that no longer describes the worktree.
func TestAnAcceptedTaskIsNotRetryable(t *testing.T) {
	store, tk := failedTask(t)
	if err := store.SetState(context.Background(), tk.ID, task.StateAccepted); err != nil {
		t.Fatalf("set accepted: %v", err)
	}

	_, err := store.Reopen(context.Background(), tk.ID)
	if err == nil {
		t.Fatal("an accepted task was reopened; its change may already be applied")
	}
	if !errors.Is(err, task.ErrNotRetryable) {
		t.Errorf("the refusal must be identifiable as ErrNotRetryable, got %v", err)
	}
	if !strings.Contains(err.Error(), "already be applied") {
		t.Errorf("the refusal must say why, got %v", err)
	}
	// And it must not have moved.
	reread, _ := store.Get(context.Background(), tk.ID)
	if reread.State != task.StateAccepted {
		t.Errorf("a refused retry changed the state to %s", reread.State)
	}
}

// Reopening something already runnable would reset a task mid-flight, which is
// a different and much worse operation than retrying a dead one.
func TestARunnableTaskIsNotReopened(t *testing.T) {
	store, tk := failedTask(t)
	for _, state := range []task.State{task.StatePending, task.StateRunning, task.StateReview} {
		if err := store.SetState(context.Background(), tk.ID, state); err != nil {
			t.Fatalf("set %s: %v", state, err)
		}
		_, err := store.Reopen(context.Background(), tk.ID)
		if err == nil {
			t.Errorf("a %s task was reopened", state)
			continue
		}
		if !errors.Is(err, task.ErrNotRetryable) {
			t.Errorf("%s: want ErrNotRetryable, got %v", state, err)
		}
	}
}

// Abandoned is a person's decision to stop, not a verdict on the work, so
// changing that decision has to be possible.
func TestAnAbandonedTaskCanBeRetried(t *testing.T) {
	store, tk := failedTask(t)
	if err := store.SetState(context.Background(), tk.ID, task.StateAbandoned); err != nil {
		t.Fatalf("set abandoned: %v", err)
	}
	if _, err := store.Reopen(context.Background(), tk.ID); err != nil {
		t.Errorf("an abandoned task must be retryable: %v", err)
	}
}

// The refusal a runner gives has to name the way forward. Without it the
// operator's only visible option is to create a new task, which is what loses
// the journal.
func TestTheRunnerPointsAtRetry(t *testing.T) {
	r, st := newRunner(t, engine.Verify{})
	store := task.NewStore(st)
	tk := task.Task{ID: task.NewID("t"), Title: "failed", Verification: recipe.Standard}
	if err := store.Create(context.Background(), tk); err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := store.SetState(context.Background(), tk.ID, task.StateFailed); err != nil {
		t.Fatalf("set failed: %v", err)
	}

	_, err := r.Run(context.Background(), tk.ID, t.TempDir())
	if err == nil {
		t.Fatal("running a failed task must be refused")
	}
	if !strings.Contains(err.Error(), "le task retry") {
		t.Errorf("the refusal must point at retry, got %v", err)
	}
}
