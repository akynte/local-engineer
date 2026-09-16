package landlock_test

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"testing"

	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
)

// Landlock restricts the calling process and cannot be undone, so each of
// these has to own its process. The parent re-executes itself with a marker in
// the environment; the child applies the ruleset and makes the assertion.
const childEnv = "LE_EPHEMERAL_TEST_CHILD"

func requireNetworkLandlock(t *testing.T) {
	t.Helper()
	if ok, why := landlock.SupportsNetwork(); !ok {
		t.Skipf("this kernel does not enforce Landlock TCP rules: %s", why)
	}
}

// A Go test suite binds port 0 and connects to whatever the kernel hands back.
// No allowlist can name that port in advance, so without the ephemeral grant
// every httptest-based test in a repository fails on a port nobody chose —
// which was 22 tests across three packages here, failing regardless of the
// change under test.
func TestEphemeralGrantLetsAnHTTPTestServerWork(t *testing.T) {
	requireNetworkLandlock(t)
	if os.Getenv(childEnv) != "ephemeral" {
		runInChild(t, "ephemeral")
		return
	}

	spec := sandbox.Spec{
		ReadWrite: []string{t.TempDir(), os.TempDir()}, Dir: t.TempDir(),
		AllowEphemeralTCP: true,
	}
	if err := landlock.Apply(spec); err != nil {
		t.Fatalf("apply: %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer srv.Close()
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatalf("an httptest server must work under the ephemeral grant: %v", err)
	}
	resp.Body.Close()
}

// The grant must not become a way around the boundary §6.2 draws. A service
// port listed in TCPDeny stays denied even though the range would cover it.
func TestADeniedServicePortIsNotOpenedByTheEphemeralGrant(t *testing.T) {
	requireNetworkLandlock(t)
	if os.Getenv(childEnv) != "deny" {
		runInChild(t, "deny")
		return
	}

	// A listener inside the ephemeral range, which the grant would otherwise
	// cover, standing in for a service an operator moved into it.
	victim, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listener: %v", err)
	}
	defer victim.Close()
	port := uint16(victim.Addr().(*net.TCPAddr).Port) //nolint:gosec // from the kernel

	spec := sandbox.Spec{
		// os.TempDir is granted so the test framework can still remove its
		// own scratch directory: Landlock outlives the assertion.
		ReadWrite: []string{t.TempDir(), os.TempDir()}, Dir: t.TempDir(),
		AllowEphemeralTCP: true,
		TCPDeny:           []uint16{port},
	}
	if err := landlock.Apply(spec); err != nil {
		t.Fatalf("apply: %v", err)
	}

	conn, err := net.Dial("tcp", victim.Addr().String())
	if err == nil {
		conn.Close()
		t.Fatalf("port %d was in TCPDeny and the ephemeral grant opened it anyway", port)
	}
}

// Without the grant the old behaviour stands: a task gets the ports it
// declared and nothing else. Turning the grant on has to be a decision rather
// than something every spec inherits.
//
// The assertion is on connect, not bind, and that is not an accident. Go's
// net.Listen has defaulted to MPTCP since 1.24 and Landlock's rules do not
// cover MPTCP sockets, so a sandboxed Go program can still *listen* on an
// unlisted port — the caveat this package's doc comment already states.
// Connect is the half that is genuinely enforced, and it is the half that
// matters here: it is what stops a task reaching a service.
func TestWithoutTheGrantAnEphemeralPortCannotBeReached(t *testing.T) {
	requireNetworkLandlock(t)
	if os.Getenv(childEnv) != "nogrant" {
		runInChild(t, "nogrant")
		return
	}

	// Opened before the ruleset applies, so this is purely a test of connect.
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("fixture listener: %v", err)
	}
	defer target.Close()

	spec := sandbox.Spec{ReadWrite: []string{t.TempDir(), os.TempDir()}, Dir: t.TempDir()}
	if err := landlock.Apply(spec); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if c, err := net.Dial("tcp", target.Addr().String()); err == nil {
		c.Close()
		t.Error("an unlisted ephemeral port was reachable without AllowEphemeralTCP")
	}
}

// The range is read from the kernel because an operator can change it, and a
// ruleset built from the wrong range fails in the confusing direction.
func TestEphemeralRangeMatchesTheKernel(t *testing.T) {
	lo, hi := landlock.EphemeralRange()
	if lo == 0 || hi == 0 || lo >= hi {
		t.Fatalf("EphemeralRange returned an unusable range: %d-%d", lo, hi)
	}
	body, err := os.ReadFile("/proc/sys/net/ipv4/ip_local_port_range")
	if err != nil {
		t.Skip("no /proc on this host; the documented default is all there is to check")
	}
	var kLo, kHi int
	if _, err := fmtSscan(string(body), &kLo, &kHi); err != nil {
		t.Skipf("unexpected /proc format %q", body)
	}
	if int(lo) != kLo || int(hi) != kHi {
		t.Errorf("EphemeralRange is %d-%d; the kernel says %d-%d", lo, hi, kLo, kHi)
	}
}

func fmtSscan(s string, a, b *int) (int, error) { return fmt.Sscan(s, a, b) }

// runInChild re-executes this test binary for one named case, so the
// irreversible ruleset it applies cannot affect any other test.
func runInChild(t *testing.T, name string) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run", "^"+t.Name()+"$", "-test.v")
	cmd.Env = append(os.Environ(), childEnv+"="+name)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("child failed: %v\n%s", err, out)
	}
}
