package mcp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The path argument comes from a model, which is the same trust level as the
// repository content that produced it. It bounds where a workspace is searched
// for; it must not be usable to reach one the client did not open.
func TestPathsOutsideTheOpenRepositoryAreRefused(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "internal", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{DataDir: t.TempDir(), WorkDir: base}}

	for _, bad := range []string{
		"..",
		"../..",
		"../" + filepath.Base(outside),
		"internal/../../escape",
		outside,                    // absolute
		filepath.Join(base, "sub"), // absolute, even inside
		"/etc/passwd",
		"internal/api/../../../../etc",
	} {
		t.Run(bad, func(t *testing.T) {
			if got, err := s.confine(bad); err == nil {
				t.Errorf("confine(%q) returned %q; it must be refused", bad, got)
			}
		})
	}
}

// A symlink inside the repository that points out of it is the interesting
// case: the join stays inside, and only resolving it reveals the escape.
func TestASymlinkOutOfTheRepositoryIsRefused(t *testing.T) {
	base := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	s := &Server{opts: Options{DataDir: t.TempDir(), WorkDir: base}}
	if got, err := s.confine("escape"); err == nil {
		t.Errorf("a symlink out of the repository resolved to %q instead of being refused", got)
	}
}

// Ordinary use has to keep working: the repository root, and a subdirectory of
// it, are both legitimate.
func TestPathsInsideTheRepositoryAreAccepted(t *testing.T) {
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "internal", "api"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := &Server{opts: Options{DataDir: t.TempDir(), WorkDir: base}}

	root, err := s.confine("")
	if err != nil {
		t.Fatalf("the repository root was refused: %v", err)
	}
	sub, err := s.confine("internal/api")
	if err != nil {
		t.Fatalf("a subdirectory was refused: %v", err)
	}
	if !strings.HasPrefix(sub, root) {
		t.Errorf("confine(%q) = %q, which is not under %q", "internal/api", sub, root)
	}
}

// A path that does not exist yet must be judged as written rather than
// accepted because EvalSymlinks could not resolve it.
func TestANonexistentEscapeIsStillRefused(t *testing.T) {
	base := t.TempDir()
	s := &Server{opts: Options{DataDir: t.TempDir(), WorkDir: base}}
	if got, err := s.confine("../does-not-exist-anywhere"); err == nil {
		t.Errorf("confine returned %q for a non-existent path outside the repository", got)
	}
}
