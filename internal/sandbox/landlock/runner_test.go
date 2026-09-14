package landlock_test

// These tests exercise the real kernel LSM. They are skipped, with a reason,
// when Landlock is unavailable — which is itself the condition DR-3 says
// `le doctor` must report rather than hide.

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
)

func requireLandlock(t *testing.T) int {
	t.Helper()
	abi, err := landlock.ABI()
	if err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	return abi
}

func TestABIIsReported(t *testing.T) {
	abi := requireLandlock(t)
	if abi < landlock.MinABI {
		t.Fatalf("ABI %d is below the minimum %d", abi, landlock.MinABI)
	}
	t.Logf("landlock ABI %d", abi)
	if ok, reason := landlock.SupportsNetwork(); !ok {
		t.Logf("TCP rules are not enforced here: %s", reason)
	}
}

// The central claim of layer 2: a task process cannot read outside its
// granted set. This runs the restriction in a real child process, because
// Landlock is irreversible within a process.
func TestSandboxedChildCannotReadOutsideItsGrantedPaths(t *testing.T) {
	requireLandlock(t)

	work := t.TempDir()
	secret := filepath.Join(t.TempDir(), "other-workspace.db")
	if err := os.WriteFile(secret, []byte("another project's index"), 0o600); err != nil {
		t.Fatal(err)
	}
	allowed := filepath.Join(work, "own.txt")
	if err := os.WriteFile(allowed, []byte("my own worktree"), 0o600); err != nil {
		t.Fatal(err)
	}

	r, err := landlock.New()
	if err != nil {
		t.Fatal(err)
	}
	// The test binary stands in for `le` as the re-exec helper: it is invoked
	// with a flag that makes it apply the spec and exec the target.
	r.Self = os.Args[0]

	spec := sandbox.Spec{
		ReadOnly:  []string{"/usr", "/bin", "/lib", "/lib64", "/etc"},
		ReadWrite: []string{work},
		Dir:       work,
		Env:       append(os.Environ(), "LE_SANDBOX_HELPER_TEST=1"),
	}

	t.Run("granted path is readable", func(t *testing.T) {
		out, err := runSandboxed(t, r, spec, "cat", allowed)
		if err != nil {
			t.Fatalf("reading a granted path failed: %v (%s)", err, out)
		}
		if !strings.Contains(out, "my own worktree") {
			t.Fatalf("unexpected output: %q", out)
		}
	})

	t.Run("path outside the grant is denied", func(t *testing.T) {
		out, err := runSandboxed(t, r, spec, "cat", secret)
		if err == nil {
			t.Fatalf("reading outside the granted set succeeded; the sandbox is not enforcing: %q", out)
		}
		if strings.Contains(out, "another project's index") {
			t.Fatalf("the sandbox leaked the file contents: %q", out)
		}
	})
}

// runSandboxed re-executes the test binary as the Landlock helper.
func runSandboxed(t *testing.T, r *landlock.Runner, spec sandbox.Spec, argv ...string) (string, error) {
	t.Helper()
	cmd, err := r.Command(context.Background(), spec, argv...)
	if err != nil {
		t.Fatal(err)
	}
	// TestHelperHelper below plays the part of cmd/le's hidden subcommand.
	cmd.Args = append([]string{cmd.Path, "-test.run=TestHelperHelper", "--"}, argv...)
	cmd.Env = append(cmd.Env, "GO_WANT_SANDBOX_HELPER=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestHelperHelper is not a test: it is the helper process entry point, using
// the standard Go pattern for exercising code that must run in a fresh process.
func TestHelperHelper(t *testing.T) {
	if os.Getenv("GO_WANT_SANDBOX_HELPER") != "1" {
		t.Skip("helper process entry point")
	}
	args := os.Args
	for i, a := range args {
		if a == "--" {
			args = args[i+1:]
			break
		}
	}
	if err := landlock.Helper(args); err != nil {
		os.Stderr.WriteString(err.Error() + "\n")
		os.Exit(1)
	}
}

func TestSpecValidationRejectsAnEmptySandbox(t *testing.T) {
	if err := (sandbox.Spec{Dir: "/x"}).Validate(); err == nil {
		t.Fatal("a spec granting no writable path must be rejected")
	}
	if err := (sandbox.Spec{ReadWrite: []string{"/x"}}).Validate(); err == nil {
		t.Fatal("a spec with no working directory must be rejected")
	}
}

// Select must choose the strongest available runner and explain the rest.
func TestSelectReportsInactiveLayersWithReasons(t *testing.T) {
	r, err := landlock.New()
	if err != nil {
		t.Fatal(err)
	}
	chosen, rep := sandbox.Select([]sandbox.Runner{r, sandbox.ContainerRunner{}})
	if chosen == nil {
		t.Fatal("Select must always fall back to the container-only runner")
	}
	if rep.Statement != sandbox.ReadmeStatement {
		t.Error("the report must carry the honest README statement")
	}
	if len(rep.Guarantees) == 0 {
		t.Error("the report must carry the §6.2 guarantee table")
	}
	// Whatever is chosen, every inactive layer must come with a reason.
	for _, n := range rep.Inactive {
		if n.Reason == "" {
			t.Errorf("layer %s reported inactive with no reason", n.Layer)
		}
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Logf("bwrap not installed here, as expected on a bare host: %v", err)
	}
}
