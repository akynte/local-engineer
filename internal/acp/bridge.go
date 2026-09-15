// Package acp is the ACP-over-TCP bridge of design v3 §4.3 and §4.1.
//
// The Agent Client Protocol is spoken over a process's stdin and stdout. An
// editor on the host cannot reach a process inside a container that way, so
// §4.1 offers two paths: `docker exec` the agent, or "the ACP-over-TCP bridge
// shipped in the image". This is the second.
//
// What the bridge deliberately does not do is understand ACP. It carries bytes
// between a TCP connection and an agent's stdio, and nothing else. That is the
// whole reason it can be trusted: a bridge that parsed the protocol would have
// a version of the protocol baked into it, and would silently corrupt or drop
// anything the version did not cover. A byte pipe cannot be wrong about a
// message shape it never inspects.
//
// A consequence of DR-7 has to be stated plainly. The design assumed OpenCode
// as the engine and `opencode acp` as the agent; what shipped is a native
// engine that does not speak ACP. So the bridge has nothing to carry unless an
// operator configures an agent command. It is off by default for that reason as
// much as for the exposure one.
package acp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os/exec"
	"sync"
	"time"
)

// Bridge accepts TCP connections and gives each one its own agent process.
type Bridge struct {
	// Command is the agent to run, argv style. Empty means no agent is
	// configured and the bridge refuses to start.
	Command []string
	// Dir is the working directory for the agent.
	Dir string
	// Env is the agent's environment.
	Env []string
	// MaxConnections bounds concurrent sessions. Each one is a process.
	MaxConnections int
	// Logf reports connections and failures.
	Logf func(format string, args ...any)

	mu     sync.Mutex
	active int
}

// ErrNoAgent is returned when no agent command is configured. It is a named
// error because it is a configuration state rather than a fault: the native
// engine does not speak ACP, so having no agent is the default condition.
var ErrNoAgent = errors.New("acp: no agent command is configured")

// DefaultMaxConnections bounds sessions; each is a process, and an editor that
// reconnects in a loop should not be able to fork-bomb the container.
const DefaultMaxConnections = 8

func (b *Bridge) logf(format string, args ...any) {
	if b.Logf != nil {
		b.Logf(format, args...)
	}
}

// Serve accepts connections until the context is cancelled.
func (b *Bridge) Serve(ctx context.Context, ln net.Listener) error {
	if len(b.Command) == 0 {
		return ErrNoAgent
	}
	if b.MaxConnections <= 0 {
		b.MaxConnections = DefaultMaxConnections
	}

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	var wg sync.WaitGroup
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				wg.Wait()
				return ctx.Err()
			}
			return fmt.Errorf("acp: accept: %w", err)
		}

		b.mu.Lock()
		over := b.active >= b.MaxConnections
		if !over {
			b.active++
		}
		b.mu.Unlock()

		if over {
			// Refusing is better than queueing: an editor gets a closed
			// connection it can report, rather than a session that never
			// starts and looks like a hang.
			b.logf("acp: refusing a connection, %d already active", b.MaxConnections)
			_ = conn.Close()
			continue
		}

		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				b.mu.Lock()
				b.active--
				b.mu.Unlock()
			}()
			if err := b.handle(ctx, conn); err != nil && !errors.Is(err, context.Canceled) {
				b.logf("acp: session ended: %v", err)
			}
		}()
	}
}

// handle runs one agent for one connection and pipes between them.
func (b *Bridge) handle(ctx context.Context, conn net.Conn) error {
	defer conn.Close()

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	//nolint:gosec // the command comes from operator configuration, not from a request
	cmd := exec.CommandContext(ctx, b.Command[0], b.Command[1:]...)
	cmd.Dir = b.Dir
	cmd.Env = b.Env

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	// The agent's stderr is its diagnostics, not protocol. Carrying it into the
	// connection would corrupt the stream the editor is parsing.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("acp: starting the agent: %w", err)
	}
	b.logf("acp: session started (%s), pid %d", b.Command[0], cmd.Process.Pid)

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				b.logf("acp: agent: %s", string(buf[:n]))
			}
			if err != nil {
				return
			}
		}
	}()

	// Both directions, and whichever ends first ends the session: a half-open
	// pipe leaves an editor waiting on an agent that has gone.
	done := make(chan error, 2)
	go func() {
		_, err := io.Copy(stdin, conn)
		_ = stdin.Close()
		done <- err
	}()
	go func() {
		_, err := io.Copy(conn, stdout)
		done <- err
	}()

	var first error
	select {
	case first = <-done:
	case <-ctx.Done():
		first = ctx.Err()
	}
	cancel()
	_ = conn.Close()

	// Give the agent a moment to exit on its own before the context kill lands,
	// so an orderly shutdown stays orderly.
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		<-waited
	}
	return first
}

// Listen opens the bridge's listener. It is separate from Serve so a caller can
// report the bound address before accepting, and so a test can use port 0.
//
// The context covers the bind itself, which is the part that can block — a
// hostname in the address means a resolver lookup. Once Listen returns, the
// listener's lifetime is Serve's concern, not this context's.
func Listen(ctx context.Context, addr string) (net.Listener, error) {
	if addr == "" {
		return nil, errors.New("acp: no address to listen on")
	}
	var lc net.ListenConfig
	return lc.Listen(ctx, "tcp", addr)
}
