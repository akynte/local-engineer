package acp_test

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/acp"
)

// The bridge carries bytes; it does not parse ACP. These tests use a stand-in
// agent that echoes, because the property under test is the transport. A test
// that spoke real ACP would be testing the stand-in.
func echoAgent() []string {
	// `cat` is the smallest possible stdio agent: whatever arrives on stdin
	// comes back on stdout, unmodified and unbuffered by line.
	return []string{"cat"}
}

func serve(t *testing.T, b *acp.Bridge) (string, context.CancelFunc) {
	t.Helper()
	ln, err := acp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := b.Serve(ctx, ln); err != nil && !errors.Is(err, context.Canceled) {
			t.Logf("serve ended: %v", err)
		}
	}()
	return ln.Addr().String(), cancel
}

// A message sent over TCP reaches the agent's stdin and its reply comes back.
// That is the whole contract.
func TestBytesGoBothWays(t *testing.T) {
	b := &acp.Bridge{Command: echoAgent(), Logf: t.Logf}
	addr, cancel := serve(t, b)
	defer cancel()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// A JSON-RPC-shaped line, to make the point that the bridge does not care.
	msg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}` + "\n"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != msg {
		t.Errorf("the bridge altered the message:\n got %q\nwant %q", line, msg)
	}
}

// Bytes must pass through untouched, including things a protocol-aware bridge
// might try to normalise.
func TestPayloadIsNotAltered(t *testing.T) {
	b := &acp.Bridge{Command: echoAgent(), Logf: t.Logf}
	addr, cancel := serve(t, b)
	defer cancel()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	// Unicode, embedded quotes, and a very long line: a bridge that parsed
	// would have opinions about all three.
	payload := `{"text":"héllo \"world\" ` + strings.Repeat("x", 5000) + `"}` + "\n"
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	got, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if got != payload {
		t.Errorf("payload altered: got %d bytes, want %d", len(got), len(payload))
	}
}

// Without an agent there is nothing to bridge. DR-5's deviation means this is
// the default state, so it must be a clear named error rather than a crash or
// a silent accept-and-hang.
func TestNoAgentIsRefusedByName(t *testing.T) {
	b := &acp.Bridge{}
	ln, err := acp.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	err = b.Serve(context.Background(), ln)
	if !errors.Is(err, acp.ErrNoAgent) {
		t.Fatalf("got %v, want ErrNoAgent", err)
	}
}

// Each connection gets its own agent process: two editors must not share one
// session's state.
func TestEachConnectionGetsItsOwnAgent(t *testing.T) {
	// An agent that reports its own pid, so two connections can be told apart.
	script := writeScript(t, `#!/bin/sh
echo "pid=$$"
cat >/dev/null
`)
	b := &acp.Bridge{Command: []string{"/bin/sh", script}, Logf: t.Logf}
	addr, cancel := serve(t, b)
	defer cancel()

	pids := map[string]bool{}
	for i := 0; i < 2; i++ {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		line, err := bufio.NewReader(conn).ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		pids[strings.TrimSpace(line)] = true
		_ = conn.Close()
	}
	if len(pids) != 2 {
		t.Errorf("two connections shared one agent process: %v", pids)
	}
}

// An editor reconnecting in a loop must not be able to fork-bomb the container.
func TestConnectionsAreBounded(t *testing.T) {
	// An agent that holds its connection open, so the slots stay occupied.
	script := writeScript(t, `#!/bin/sh
cat >/dev/null
`)
	b := &acp.Bridge{Command: []string{"/bin/sh", script}, MaxConnections: 2, Logf: t.Logf}
	addr, cancel := serve(t, b)
	defer cancel()

	var held []net.Conn
	defer func() {
		for _, c := range held {
			_ = c.Close()
		}
	}()
	for i := 0; i < 2; i++ {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, c)
	}
	// Let the two be accepted before testing the third.
	time.Sleep(300 * time.Millisecond)

	third, err := net.Dial("tcp", addr)
	if err != nil {
		// A refused dial is also an acceptable way to say no.
		return
	}
	defer third.Close()
	_ = third.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 1)
	if _, err := third.Read(buf); err == nil {
		t.Error("a third connection was served despite a limit of two")
	}
}

// The agent's stderr is diagnostics, not protocol: carrying it into the
// connection would corrupt the stream the editor is parsing.
func TestAgentStderrDoesNotReachTheConnection(t *testing.T) {
	script := writeScript(t, `#!/bin/sh
echo "a warning nobody asked for" >&2
cat
`)
	var logged strings.Builder
	b := &acp.Bridge{
		Command: []string{"/bin/sh", script},
		Logf:    func(f string, a ...any) { fmt.Fprintf(&logged, f+"\n", a...) },
	}
	addr, cancel := serve(t, b)
	defer cancel()

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	msg := "{\"jsonrpc\":\"2.0\"}\n"
	if _, err := conn.Write([]byte(msg)); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(line, "warning") {
		t.Errorf("the agent's stderr was carried into the protocol stream: %q", line)
	}
	if line != msg {
		t.Errorf("got %q, want the echoed message", line)
	}
}

func writeScript(t *testing.T, body string) string {
	t.Helper()
	p := t.TempDir() + "/agent.sh"
	if err := os.WriteFile(p, []byte(body), 0o700); err != nil {
		t.Fatal(err)
	}
	return p
}
