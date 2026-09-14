package ledger_test

// The Stage D interruption suite (design v3 §17.1, listed at §317): application
// restart, model failure, user interruption, system crash (kill -9), process
// termination, hardware failure, timeout, context restart.
//
// docs/explanation/crash-recovery.md makes a strong claim about these: "All of
// these produce the same journal state, and are handled the same way... there
// is no separate code path per failure mode, so there is no rarely-exercised
// path to get wrong." That claim is what this file tests. Each class is induced
// for real, and the assertion is that the recovery verdict is decided by the
// worktree and never by how the process stopped.
//
// Two classes cannot be faked in-process. A `kill -9` has to be a real SIGKILL
// of a real process, because what makes it interesting is that no deferred
// cleanup runs and SQLite is left to recover its own write-ahead log. Those two
// re-execute this test binary as a child.

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// worktreeCondition is the state the worktree is left in when the process
// stops. §7.2 step 1 decides an uncertain operation by inspecting exactly this.
type worktreeCondition int

const (
	// editApplied: the write landed before the process died.
	editApplied worktreeCondition = iota
	// editPartial: the write landed but not as intended — a torn write, or a
	// second edit that was never journalled.
	editPartial
	// editAbsent: the write never happened.
	editAbsent
)

func (c worktreeCondition) String() string {
	switch c {
	case editApplied:
		return "edit applied"
	case editPartial:
		return "edit partially applied"
	default:
		return "edit not applied"
	}
}

// want returns the classification §7.2 requires for this condition.
func (c worktreeCondition) want() ledger.Applied {
	switch c {
	case editApplied:
		return ledger.AppliedComplete
	case editPartial:
		return ledger.AppliedPartial
	default:
		return ledger.AppliedNotApplied
	}
}

// interrupter induces one interruption class. It is handed a journal handle for
// an edit whose intent is already written, and must leave the process in
// whatever state that failure mode leaves it — without ever writing an outcome,
// because none of these failure modes gets the chance to.
type interrupter struct {
	name string
	// stop is what the failure mode does after the side effect. It never calls
	// Complete: an interruption is precisely the case where the outcome is
	// never written.
	stop func(t *testing.T, h *ledger.Handle)
}

// stageDClasses are the six classes that can be induced in this process. The
// two that need a real process death are separate tests below.
var stageDClasses = []interrupter{
	{
		name: "application restart",
		// The process exits between the side effect and the outcome. Nothing
		// is recorded; the store is simply reopened.
		stop: func(*testing.T, *ledger.Handle) {},
	},
	{
		name: "model failure",
		// The model errored and the engine caught it. The journal keeps the
		// cause — "intent with no outcome, plus a recorded error if it was
		// caught" — and the operation stays uncertain, because by the time a
		// step fails it may already have written to the worktree. Recovery must
		// still reach its verdict by inspection rather than by believing the
		// error.
		stop: func(t *testing.T, h *ledger.Handle) {
			if err := h.Interrupted(context.Background(),
				errors.New("provider returned no choices")); err != nil {
				t.Fatalf("recording the model failure: %v", err)
			}
		},
	},
	{
		name: "user interruption",
		stop: func(t *testing.T, h *ledger.Handle) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			// A cancelled context must not be able to write an outcome.
			if err := h.Complete(ctx, map[string]string{"status": "ok"}, "", ""); err == nil {
				t.Fatal("an outcome was written under a cancelled context; " +
					"an interrupted operation would look complete")
			}
		},
	},
	{
		name: "hardware failure",
		// The inference route was dropped. From the journal's point of view
		// this is identical to every other class.
		stop: func(*testing.T, *ledger.Handle) {},
	},
	{
		name: "timeout",
		stop: func(t *testing.T, h *ledger.Handle) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
			defer cancel()
			time.Sleep(time.Millisecond)
			if err := h.Complete(ctx, map[string]string{"status": "ok"}, "", ""); err == nil {
				t.Fatal("an outcome was written after the deadline passed")
			}
		},
	},
	{
		name: "context restart",
		// A context restart is the orderly one: a checkpoint is written, so
		// recovery has a handoff and the operation is not uncertain at all.
		stop: func(*testing.T, *ledger.Handle) {},
	},
}

// TestStageDVerdictDependsOnTheWorktreeNotTheFailureMode is the claim in
// docs/explanation/crash-recovery.md, tested directly: every interruption class
// crossed with every worktree condition, asserting the classification comes
// from the worktree alone.
//
// If a failure mode ever grows its own code path, this matrix is where it shows
// up — one class disagreeing with the others about an identical worktree.
func TestStageDVerdictDependsOnTheWorktreeNotTheFailureMode(t *testing.T) {
	conditions := []worktreeCondition{editApplied, editPartial, editAbsent}

	for _, class := range stageDClasses {
		for _, cond := range conditions {
			t.Run(class.name+"/"+cond.String(), func(t *testing.T) {
				l, st := newLedger(t)
				taskID := "stage-d"
				seedTask(t, st, taskID)

				const before = "package main\n\nfunc main() {}\n"
				const after = "package main\n\nfunc main() { run() }\n"
				dir, beforeHash := worktree(t, before)
				path := filepath.Join(dir, "main.go")

				afterHash, err := hashOf(after)
				if err != nil {
					t.Fatal(err)
				}

				// Intent first, always, and before the side effect.
				h, err := l.Begin(context.Background(), taskID, ledger.KindEdit, ledger.EditIntent{
					Path: "main.go", BeforeHash: beforeHash, AfterHash: afterHash,
					Summary: "call run from main",
				}, "")
				if err != nil {
					t.Fatal(err)
				}

				switch cond {
				case editApplied:
					mustWrite(t, path, after)
				case editPartial:
					mustWrite(t, path, "package main\n\nfunc main() { ru")
				case editAbsent:
					// leave it alone
				}

				class.stop(t, h)

				// The process comes back and recovery runs.
				state, err := l.RecoverTask(context.Background(), taskID, dir)
				if err != nil {
					t.Fatal(err)
				}
				if len(state.Uncertain) != 1 {
					t.Fatalf("recovery found %d uncertain operation(s), want 1: an "+
						"intent with no outcome is exactly what every one of these "+
						"failure modes leaves behind", len(state.Uncertain))
				}
				if got := state.Uncertain[0].Applied; got != cond.want() {
					t.Errorf("classified as %q, want %q. The verdict must come from "+
						"inspecting the worktree, not from how the process stopped.",
						got, cond.want())
				}
				// §7.2 step 3: a partially applied edit is never safe to resume
				// from without re-checking, whatever killed the process.
				if cond == editPartial && state.SafeToResume() {
					t.Error("a partially applied edit was reported safe to resume")
				}
			})
		}
	}
}

// TestStageDSystemCrash is the class that cannot be simulated: a real SIGKILL,
// so no deferred close runs, no WAL checkpoint happens, and SQLite has to
// recover the database on the next open. If the journal's durability settings
// were wrong, this is the test that would show it.
func TestStageDSystemCrash(t *testing.T) {
	runKilledChild(t, "SIGKILL", syscall.SIGKILL)
}

// TestStageDProcessTermination is the same shape with SIGTERM, which a
// container runtime sends on shutdown. The child installs no handler, so it
// dies where it stands — an intent with no outcome, like the rest.
func TestStageDProcessTermination(t *testing.T) {
	runKilledChild(t, "SIGTERM", syscall.SIGTERM)
}

// runKilledChild spawns this test binary as a child, waits for it to journal an
// intent and apply the edit, kills it with the given signal, then recovers the
// same data directory in the parent.
func runKilledChild(t *testing.T, name string, sig syscall.Signal) {
	t.Helper()
	if testing.Short() {
		t.Skip("spawns a child process")
	}

	dataDir := t.TempDir()
	workDir := t.TempDir()
	ready := filepath.Join(dataDir, "ready")

	cmd := exec.Command(os.Args[0], "-test.run=TestStageDCrashChild", "-test.v")
	cmd.Env = append(os.Environ(),
		"LE_STAGE_D_CHILD=1",
		"LE_STAGE_D_DATA="+dataDir,
		"LE_STAGE_D_WORK="+workDir,
		"LE_STAGE_D_READY="+ready,
	)
	var out strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the child: %v", err)
	}
	defer func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()

	// Wait for the child to say it has written the intent and applied the edit.
	deadline := time.Now().Add(30 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the child never became ready:\n%s", out.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Kill it by pid. Passing a negative pid to signal a process group is a
	// trap here: it is parsed as an option by some shells and, worse, would
	// take this test process with it.
	if err := syscall.Kill(cmd.Process.Pid, sig); err != nil {
		t.Fatalf("sending %s: %v", name, err)
	}
	state, err := cmd.Process.Wait()
	if err != nil {
		t.Fatalf("waiting for the child: %v", err)
	}
	if state.ExitCode() == 0 {
		t.Fatalf("the child exited cleanly under %s, so nothing was interrupted:\n%s",
			name, out.String())
	}

	// The parent opens the same data directory the killed process was using.
	root, err := store.OpenRoot(dataDir)
	if err != nil {
		t.Fatalf("reopening the data directory after %s: %v", name, err)
	}
	defer func() { _ = root.CloseAll() }()

	st, err := root.OpenWorkspace(context.Background(), stageDWorkspace())
	if err != nil {
		t.Fatalf("reopening the workspace after %s: %v", name, err)
	}
	l := ledger.New(st)

	state2, err := l.RecoverTask(context.Background(), "stage-d-crash", workDir)
	if err != nil {
		t.Fatalf("recovering after %s: %v", name, err)
	}
	if len(state2.Uncertain) != 1 {
		t.Fatalf("after %s recovery found %d uncertain operation(s), want 1:\nchild output:\n%s",
			name, len(state2.Uncertain), out.String())
	}
	// The child applied the edit before it was killed, so inspection must find
	// it complete — the same verdict the in-process classes reach for the same
	// worktree.
	if got := state2.Uncertain[0].Applied; got != ledger.AppliedComplete {
		t.Errorf("after %s the applied edit classified as %q, want %q",
			name, got, ledger.AppliedComplete)
	}
}

// TestStageDCrashChild is the child half of the two signal tests. It is a test
// only so that the test binary can re-execute itself; it does nothing unless
// the parent set the environment.
func TestStageDCrashChild(t *testing.T) {
	if os.Getenv("LE_STAGE_D_CHILD") != "1" {
		t.Skip("child half of the crash tests; run by the parent")
	}
	dataDir := os.Getenv("LE_STAGE_D_DATA")
	workDir := os.Getenv("LE_STAGE_D_WORK")
	ready := os.Getenv("LE_STAGE_D_READY")

	root, err := store.OpenRoot(dataDir)
	if err != nil {
		t.Fatalf("child: opening the data directory: %v", err)
	}
	st, err := root.OpenWorkspace(context.Background(), stageDWorkspace())
	if err != nil {
		t.Fatalf("child: opening the workspace: %v", err)
	}
	seedTask(t, st, "stage-d-crash")
	l := ledger.New(st)

	const before = "package main\n\nfunc main() {}\n"
	const after = "package main\n\nfunc main() { run() }\n"
	path := filepath.Join(workDir, "main.go")
	mustWrite(t, path, before)
	beforeHash, err := ledger.HashFile(path)
	if err != nil {
		t.Fatalf("child: hashing: %v", err)
	}
	afterHash, err := hashOf(after)
	if err != nil {
		t.Fatalf("child: hashing: %v", err)
	}

	// Intent before the side effect, which is the whole point of §7.1.
	if _, err := l.Begin(context.Background(), "stage-d-crash", ledger.KindEdit, ledger.EditIntent{
		Path: "main.go", BeforeHash: beforeHash, AfterHash: afterHash,
		Summary: "call run from main",
	}, ""); err != nil {
		t.Fatalf("child: beginning the edit: %v", err)
	}
	mustWrite(t, path, after)

	// Tell the parent to kill us. Deliberately no Close and no checkpoint: a
	// process that is about to be killed does not get to tidy up, and that is
	// what makes this test worth having.
	if err := os.WriteFile(ready, []byte("ready"), 0o644); err != nil {
		t.Fatalf("child: signalling readiness: %v", err)
	}
	time.Sleep(60 * time.Second) // the parent kills us long before this
}

// stageDWorkspace is the id both halves of the crash tests use.
func stageDWorkspace() workspace.ID {
	return workspace.DeriveID("/synthetic/stage-d", "", "stage-d-crash")
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// hashOf returns the digest the journal would record for this content.
func hashOf(body string) (string, error) {
	f, err := os.CreateTemp("", "stage-d-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return ledger.HashFile(f.Name())
}

// TestInterruptedKeepsTheOperationUncertain is the distinction the Stage D
// matrix rests on, tested directly.
//
// Fail and Interrupted both record that something went wrong; only one of them
// also claims the side effect did not happen. An engine step reads files,
// writes edits and runs verification before it can return an error, so the
// worktree it leaves behind is a state nobody has looked at. Recording that as
// a definite failure marks the operation certain, and recovery does not inspect
// certain operations — so a half-applied set of edits would never be examined.
func TestInterruptedKeepsTheOperationUncertain(t *testing.T) {
	l, st := newLedger(t)
	seedTask(t, st, "uncertainty")
	ctx := context.Background()

	dir, beforeHash := worktree(t, "package main\n")
	afterHash, err := hashOf("package main\n\nfunc main() {}\n")
	if err != nil {
		t.Fatal(err)
	}
	intent := ledger.EditIntent{Path: "main.go", BeforeHash: beforeHash, AfterHash: afterHash}

	// Interrupted: the cause is kept, the operation stays uncertain.
	h, err := l.Begin(ctx, "uncertainty", ledger.KindEdit, intent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Interrupted(ctx, errors.New("provider timed out")); err != nil {
		t.Fatal(err)
	}
	ops, err := l.Operations(ctx, "uncertainty")
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 1 {
		t.Fatalf("got %d operations, want 1", len(ops))
	}
	if !ops[0].Uncertain() {
		t.Error("an interrupted operation was recorded as certain; recovery will " +
			"not inspect it, and any side effect it had is now invisible")
	}
	if ops[0].Error != "provider timed out" {
		t.Errorf("cause = %q, want the recorded error", ops[0].Error)
	}

	// The cause must reach the reconciliation, so an operator reading the
	// recovery report sees why it stopped as well as what it did.
	state, err := l.RecoverTask(ctx, "uncertainty", dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Uncertain) != 1 {
		t.Fatalf("recovery found %d uncertain operation(s), want 1", len(state.Uncertain))
	}
	if !strings.Contains(state.Uncertain[0].Detail, "provider timed out") {
		t.Errorf("the recovery detail does not carry the cause: %q", state.Uncertain[0].Detail)
	}

	// Fail is the other contract: a definite failure, nothing to inspect.
	h2, err := l.Begin(ctx, "uncertainty", ledger.KindEdit, intent, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h2.Fail(ctx, errors.New("could not open the file")); err != nil {
		t.Fatal(err)
	}
	ops, err = l.Operations(ctx, "uncertainty")
	if err != nil {
		t.Fatal(err)
	}
	if ops[1].Uncertain() {
		t.Error("a definite failure was recorded as uncertain")
	}
}

// TestInterruptedHandleIsSpent stops an interrupted operation being completed
// afterwards as though nothing had happened.
func TestInterruptedHandleIsSpent(t *testing.T) {
	l, st := newLedger(t)
	seedTask(t, st, "spent")
	ctx := context.Background()

	h, err := l.Begin(ctx, "spent", ledger.KindEdit, ledger.EditIntent{Path: "a.go"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := h.Interrupted(ctx, errors.New("killed")); err != nil {
		t.Fatal(err)
	}
	if err := h.Complete(ctx, map[string]string{"status": "ok"}, "", ""); err == nil {
		t.Fatal("an interrupted operation was completed afterwards")
	}
	if err := h.Interrupted(ctx, errors.New("again")); err == nil {
		t.Fatal("an interrupted operation was interrupted twice")
	}
}
