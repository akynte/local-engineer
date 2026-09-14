// Package procman supervises the long-lived children of the single container
// (design v3 §4.3).
//
// DR-1's stated disadvantage is that multiple processes under one PID 1 reduce
// orchestrator visibility. This package is the compensation: every child has a
// declared health check, a restart policy with backoff, and an orderly
// shutdown path, and the supervisor reports all of it on /healthz and /readyz
// so an operator sees per-child state the way compose or Kubernetes would show
// per-container state.
//
// §4.3 also requires that the supervisor's child management sit behind an
// interface, so that running llama-server outside the container is a
// configuration change rather than a code change. That is what Child is.
package procman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// State is a child's lifecycle state.
type State string

const (
	StateStopped  State = "stopped"
	StateStarting State = "starting"
	StateReady    State = "ready"
	StateDegraded State = "degraded" // running, failing its health check
	StateFailed   State = "failed"   // exhausted its restart budget
	StateStopping State = "stopping"
)

// Terminal reports whether no further transition is expected without operator
// action.
func (s State) Terminal() bool { return s == StateFailed || s == StateStopped }

// Child declares one supervised process.
type Child struct {
	// Name identifies the child in logs and health output.
	Name string
	// Build constructs the command. It is a function rather than a *exec.Cmd
	// because a restart needs a fresh one.
	Build func(ctx context.Context) (*exec.Cmd, error)
	// Health probes readiness. Nil means "ready as soon as it is running",
	// which is only honest for children with no startup work.
	Health func(ctx context.Context) error
	// HealthInterval between probes. Zero means DefaultHealthInterval.
	HealthInterval time.Duration
	// StartTimeout bounds the wait for the first successful health probe.
	// Model load on a cold GGUF is slow, so this is per-child.
	StartTimeout time.Duration
	// MaxRestarts within RestartWindow before the child is marked failed.
	// Zero means DefaultMaxRestarts.
	MaxRestarts int
	// RestartWindow over which restarts are counted.
	RestartWindow time.Duration
	// Essential children fail the whole supervisor when they reach
	// StateFailed. A non-essential child's failure degrades readiness only.
	Essential bool
	// StopSignal defaults to SIGTERM.
	StopSignal syscall.Signal
	// StopGrace is how long to wait after StopSignal before SIGKILL.
	StopGrace time.Duration
}

// Defaults for the fields a Child leaves zero.
const (
	DefaultHealthInterval = 5 * time.Second
	DefaultStartTimeout   = 60 * time.Second
	DefaultMaxRestarts    = 5
	DefaultRestartWindow  = 5 * time.Minute
	DefaultStopGrace      = 10 * time.Second
	// MaxBackoff caps exponential restart backoff. A child that cannot start
	// should be retried occasionally, not hammered.
	MaxBackoff = 30 * time.Second
)

// Status is one child's observable state.
type Status struct {
	Name       string    `json:"name"`
	State      State     `json:"state"`
	PID        int       `json:"pid,omitempty"`
	Restarts   int       `json:"restarts"`
	LastError  string    `json:"last_error,omitempty"`
	LastChange time.Time `json:"last_change"`
	Essential  bool      `json:"essential"`
}

// Manager supervises a set of children.
type Manager struct {
	mu       sync.RWMutex
	children map[string]*supervised
	order    []string

	logf func(format string, args ...any)

	started bool
	cancel  context.CancelFunc
	wg      sync.WaitGroup
}

type supervised struct {
	spec   Child
	status Status

	mu  sync.Mutex
	cmd *exec.Cmd
}

// New builds a manager. logf may be nil.
func New(logf func(string, ...any)) *Manager {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Manager{children: map[string]*supervised{}, logf: logf}
}

// Add registers a child. It must be called before Start.
func (m *Manager) Add(c Child) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return errors.New("procman: children must be registered before Start")
	}
	if c.Name == "" {
		return errors.New("procman: child has no name")
	}
	if c.Build == nil {
		return fmt.Errorf("procman: child %s has no Build function", c.Name)
	}
	if _, exists := m.children[c.Name]; exists {
		return fmt.Errorf("procman: child %s is already registered", c.Name)
	}
	applyDefaults(&c)
	m.children[c.Name] = &supervised{
		spec:   c,
		status: Status{Name: c.Name, State: StateStopped, Essential: c.Essential, LastChange: time.Now()},
	}
	m.order = append(m.order, c.Name)
	return nil
}

func applyDefaults(c *Child) {
	if c.HealthInterval == 0 {
		c.HealthInterval = DefaultHealthInterval
	}
	if c.StartTimeout == 0 {
		c.StartTimeout = DefaultStartTimeout
	}
	if c.MaxRestarts == 0 {
		c.MaxRestarts = DefaultMaxRestarts
	}
	if c.RestartWindow == 0 {
		c.RestartWindow = DefaultRestartWindow
	}
	if c.StopGrace == 0 {
		c.StopGrace = DefaultStopGrace
	}
	if c.StopSignal == 0 {
		c.StopSignal = syscall.SIGTERM
	}
}

// Start launches every registered child and supervises them until the context
// is cancelled or Stop is called.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	if m.started {
		m.mu.Unlock()
		return errors.New("procman: already started")
	}
	m.started = true
	ctx, m.cancel = context.WithCancel(ctx)
	names := append([]string(nil), m.order...)
	m.mu.Unlock()

	for _, name := range names {
		m.mu.RLock()
		s := m.children[name]
		m.mu.RUnlock()
		m.wg.Add(1)
		go func(s *supervised) {
			defer m.wg.Done()
			m.supervise(ctx, s)
		}(s)
	}
	return nil
}

// supervise runs one child's start/health/restart loop.
func (m *Manager) supervise(ctx context.Context, s *supervised) {
	var restartTimes []time.Time
	backoff := time.Second

	for {
		if ctx.Err() != nil {
			return
		}
		// Prune the restart window before deciding whether the budget is spent.
		cutoff := time.Now().Add(-s.spec.RestartWindow)
		kept := restartTimes[:0]
		for _, t := range restartTimes {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		restartTimes = kept

		if len(restartTimes) >= s.spec.MaxRestarts {
			m.setState(s, StateFailed, fmt.Errorf("restarted %d times within %s; giving up",
				len(restartTimes), s.spec.RestartWindow))
			m.logf("procman: child %s failed permanently", s.spec.Name)
			return
		}

		restartTimes = append(restartTimes, time.Now())
		exitErr := m.runOnce(ctx, s)
		if ctx.Err() != nil {
			return
		}

		m.logf("procman: child %s exited: %v; restarting in %s", s.spec.Name, exitErr, backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff *= 2
		if backoff > MaxBackoff {
			backoff = MaxBackoff
		}
		m.mu.Lock()
		s.status.Restarts++
		m.mu.Unlock()
	}
}

// runOnce starts the child, waits for health, then watches until it exits.
func (m *Manager) runOnce(ctx context.Context, s *supervised) error {
	m.setState(s, StateStarting, nil)

	cmd, err := s.spec.Build(ctx)
	if err != nil {
		m.setState(s, StateStarting, err)
		return err
	}
	// A child gets its own process group so that stopping it takes its
	// descendants with it: a test runner that spawns helpers must not outlive
	// the task.
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true

	if err := cmd.Start(); err != nil {
		m.setState(s, StateStarting, err)
		return err
	}
	s.mu.Lock()
	s.cmd = cmd
	s.mu.Unlock()

	m.mu.Lock()
	s.status.PID = cmd.Process.Pid
	m.mu.Unlock()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	if err := m.waitReady(ctx, s, done); err != nil {
		m.stopChild(s)
		<-done
		return err
	}

	// Ready: watch health until the process exits or the context ends.
	ticker := time.NewTicker(s.spec.HealthInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.stopChild(s)
			<-done
			return ctx.Err()
		case err := <-done:
			m.setState(s, StateStopped, err)
			return err
		case <-ticker.C:
			if s.spec.Health == nil {
				continue
			}
			hctx, cancel := context.WithTimeout(ctx, s.spec.HealthInterval)
			err := s.spec.Health(hctx)
			cancel()
			if err != nil {
				m.setState(s, StateDegraded, err)
			} else {
				m.setState(s, StateReady, nil)
			}
		}
	}
}

func (m *Manager) waitReady(ctx context.Context, s *supervised, exited <-chan error) error {
	if s.spec.Health == nil {
		m.setState(s, StateReady, nil)
		return nil
	}
	deadline := time.After(s.spec.StartTimeout)
	probe := time.NewTicker(500 * time.Millisecond)
	defer probe.Stop()

	var last error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-exited:
			return fmt.Errorf("procman: child %s exited before becoming ready: %w", s.spec.Name, err)
		case <-deadline:
			return fmt.Errorf("procman: child %s did not become ready within %s (last probe: %v)",
				s.spec.Name, s.spec.StartTimeout, last)
		case <-probe.C:
			hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			last = s.spec.Health(hctx)
			cancel()
			if last == nil {
				m.setState(s, StateReady, nil)
				return nil
			}
		}
	}
}

// stopChild signals the child's process group, then escalates after the grace
// period. §4.4: SIGTERM, a documented grace period, then exit.
func (m *Manager) stopChild(s *supervised) {
	s.mu.Lock()
	cmd := s.cmd
	s.mu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	m.setState(s, StateStopping, nil)

	pgid := -cmd.Process.Pid // negative pid signals the whole group
	_ = syscall.Kill(pgid, s.spec.StopSignal)

	deadline := time.After(s.spec.StopGrace)
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline:
			m.logf("procman: child %s did not exit within %s; sending SIGKILL", s.spec.Name, s.spec.StopGrace)
			_ = syscall.Kill(pgid, syscall.SIGKILL)
			return
		case <-tick.C:
			if err := syscall.Kill(pgid, 0); err != nil {
				return // the group is gone
			}
		}
	}
}

func (m *Manager) setState(s *supervised, st State, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s.status.State != st {
		s.status.LastChange = time.Now()
	}
	s.status.State = st
	if err != nil {
		s.status.LastError = err.Error()
	} else if st == StateReady {
		s.status.LastError = ""
	}
	if st == StateStopped || st == StateFailed {
		s.status.PID = 0
	}
}

// Status reports every child, in registration order.
func (m *Manager) Status() []Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Status, 0, len(m.order))
	for _, name := range m.order {
		out = append(out, m.children[name].status)
	}
	return out
}

// Ready reports whether every essential child is ready, and why not.
func (m *Manager) Ready() (bool, []string) {
	var reasons []string
	for _, st := range m.Status() {
		if !st.Essential {
			continue
		}
		if st.State != StateReady {
			reason := fmt.Sprintf("%s is %s", st.Name, st.State)
			if st.LastError != "" {
				reason += ": " + st.LastError
			}
			reasons = append(reasons, reason)
		}
	}
	return len(reasons) == 0, reasons
}

// Stop signals every child and waits for the supervision loops to finish.
// It is the SIGTERM path of §4.4.
func (m *Manager) Stop(timeout time.Duration) error {
	m.mu.Lock()
	cancel := m.cancel
	children := make([]*supervised, 0, len(m.order))
	for _, name := range m.order {
		children = append(children, m.children[name])
	}
	m.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	// Stop in reverse registration order: dependents before dependencies.
	for i := len(children) - 1; i >= 0; i-- {
		m.stopChild(children[i])
	}

	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-time.After(timeout):
		return fmt.Errorf("procman: children did not stop within %s", timeout)
	}
}

// Reap adopts orphaned processes when running as PID 1. §4.3 offers `tini` or
// Docker's --init as the usual answer; when neither is present, `le` must reap
// or the container accumulates zombies.
func (m *Manager) Reap(ctx context.Context) {
	if os.Getpid() != 1 {
		return
	}
	ch := make(chan os.Signal, 8)
	notifyChild(ch)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			for {
				var ws syscall.WaitStatus
				pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
				if pid <= 0 || err != nil {
					break
				}
			}
		}
	}
}
