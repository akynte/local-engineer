package procman_test

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/procman"
)

func TestChildBecomesReadyAndIsReported(t *testing.T) {
	m := procman.New(t.Logf)
	var healthy atomic.Bool

	err := m.Add(procman.Child{
		Name:      "sleeper",
		Essential: true,
		Build: func(ctx context.Context) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sleep", "30"), nil
		},
		Health: func(context.Context) error {
			if healthy.Load() {
				return nil
			}
			return errors.New("not yet")
		},
		HealthInterval: 50 * time.Millisecond,
		StartTimeout:   3 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	// Before the health probe passes, readiness must be false with a reason.
	if ready, reasons := m.Ready(); ready || len(reasons) == 0 {
		t.Fatalf("expected not-ready with a reason, got ready=%v reasons=%v", ready, reasons)
	}

	healthy.Store(true)
	waitFor(t, 3*time.Second, func() bool { ready, _ := m.Ready(); return ready })

	st := m.Status()
	if len(st) != 1 || st[0].State != procman.StateReady || st[0].PID == 0 {
		t.Fatalf("unexpected status: %+v", st)
	}

	if err := m.Stop(5 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
}

func TestChildThatNeverBecomesReadyIsRestartedThenFailed(t *testing.T) {
	m := procman.New(t.Logf)
	var builds atomic.Int32

	err := m.Add(procman.Child{
		Name:      "never-ready",
		Essential: true,
		Build: func(ctx context.Context) (*exec.Cmd, error) {
			builds.Add(1)
			return exec.CommandContext(ctx, "sleep", "30"), nil
		},
		Health:        func(context.Context) error { return errors.New("never ready") },
		StartTimeout:  200 * time.Millisecond,
		MaxRestarts:   2,
		RestartWindow: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}

	waitFor(t, 15*time.Second, func() bool {
		return m.Status()[0].State == procman.StateFailed
	})
	if got := builds.Load(); got != 2 {
		t.Errorf("built %d times, expected the restart budget of 2", got)
	}
	if ready, reasons := m.Ready(); ready {
		t.Error("a failed essential child must keep readiness false")
	} else if len(reasons) == 0 {
		t.Error("readiness failure must come with a reason")
	}
	_ = m.Stop(5 * time.Second)
}

func TestStopTerminatesTheWholeProcessGroup(t *testing.T) {
	m := procman.New(t.Logf)
	// A shell that spawns a long-lived grandchild: stopping the child must
	// take the grandchild with it, or a test runner would outlive its task.
	err := m.Add(procman.Child{
		Name: "group",
		Build: func(ctx context.Context) (*exec.Cmd, error) {
			return exec.CommandContext(ctx, "sh", "-c", "sleep 60 & wait"), nil
		},
		StopGrace: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 3*time.Second, func() bool { return m.Status()[0].PID != 0 })
	pid := m.Status()[0].PID

	start := time.Now()
	if err := m.Stop(10 * time.Second); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("stop took %s; the grace escalation is not working", time.Since(start))
	}
	// The process group must be gone. syscall.Kill with signal 0 is the
	// probe; the `kill` binary would parse a negative pid as an option.
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Error("the child's process group survived Stop")
	} else if !errors.Is(err, syscall.ESRCH) {
		t.Errorf("unexpected probe error: %v", err)
	}
}

func TestRegistrationValidations(t *testing.T) {
	m := procman.New(nil)
	if err := m.Add(procman.Child{Name: ""}); err == nil {
		t.Error("a nameless child must be rejected")
	}
	if err := m.Add(procman.Child{Name: "x"}); err == nil {
		t.Error("a child with no Build must be rejected")
	}
	build := func(ctx context.Context) (*exec.Cmd, error) { return exec.CommandContext(ctx, "true"), nil }
	if err := m.Add(procman.Child{Name: "x", Build: build}); err != nil {
		t.Fatal(err)
	}
	if err := m.Add(procman.Child{Name: "x", Build: build}); err == nil {
		t.Error("a duplicate name must be rejected")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_ = m.Start(ctx)
	if err := m.Add(procman.Child{Name: "y", Build: build}); err == nil {
		t.Error("registration after Start must be rejected")
	}
	_ = m.Stop(2 * time.Second)
}

func waitFor(t *testing.T, limit time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(limit)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", limit)
}
