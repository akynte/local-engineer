package opencode_test

// This exercises the real kernel LSM against the spec a session actually runs
// under, rather than against a spec written for the test. Confine grants /proc
// and /sys — a managed runtime reads its own memory map before it runs any of
// the program's code, and denying that produces a silent exit rather than a
// permission error — so the claim that the session still cannot reach the
// developer's files is one worth checking rather than asserting.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/landlock"
)

func TestAConfinedSessionCannotReadOutsideTheWorkspace(t *testing.T) {
	if _, err := landlock.ABI(); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}

	s, _ := session(t)
	// Stands in for what the sandbox exists to keep out: a credential in the
	// developer's home, which no path in the session's grant names.
	home := t.TempDir()
	secret := filepath.Join(home, ".aws-credentials")
	if err := os.WriteFile(secret, []byte("aws_secret_access_key = real"), 0o600); err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(s.Repo, "main.go")
	if err := os.WriteFile(own, []byte("package main // the task's own file"), 0o600); err != nil {
		t.Fatal(err)
	}

	spec, err := s.Confine(sandbox.Spec{ReadOnly: []string{"/usr", "/bin", "/lib", "/lib64", "/etc"}})
	if err != nil {
		t.Fatal(err)
	}
	// The child needs the test binary's own environment to re-exec as the
	// helper; Confine's scrubbed environment is asserted separately.
	spec.Env = append(os.Environ(), "LE_SANDBOX_HELPER_TEST=1")

	r, err := landlock.New()
	if err != nil {
		t.Fatal(err)
	}
	r.Self = os.Args[0]

	if out, err := runConfined(t, r, spec, "cat", own); err != nil {
		t.Fatalf("the session cannot read its own worktree: %v (%s)", err, out)
	} else if !strings.Contains(out, "the task's own file") {
		t.Fatalf("unexpected output: %q", out)
	}

	out, err := runConfined(t, r, spec, "cat", secret)
	if err == nil {
		t.Fatalf("the session read a credential outside its grant: %q", out)
	}
	if strings.Contains(out, "real") {
		t.Fatalf("the sandbox leaked the file contents: %q", out)
	}
}

func runConfined(t *testing.T, r *landlock.Runner, spec sandbox.Spec, argv ...string) (string, error) {
	t.Helper()
	cmd, err := r.Command(context.Background(), spec, argv...)
	if err != nil {
		t.Fatal(err)
	}
	cmd.Args = append([]string{cmd.Path, "-test.run=TestConfineHelper", "--"}, argv...)
	cmd.Env = append(cmd.Env, "GO_WANT_SANDBOX_HELPER=1")
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestConfineHelper is not a test: it is the helper process entry point, using
// the standard Go pattern for code that must run in a fresh process because
// Landlock is irreversible within one.
func TestConfineHelper(t *testing.T) {
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
