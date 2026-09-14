package worktree_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/worktree"
)

// Confinement lives here rather than in each caller, so it is tested here.
// The engine writes paths a model supplied, which makes this the check most
// worth having a direct test.

func setup(t *testing.T) (wt, outside string) {
	t.Helper()
	wt = t.TempDir()
	outside = t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, "inside.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret.txt"), []byte("not mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	return wt, outside
}

func TestResolveAcceptsPathsInside(t *testing.T) {
	wt, _ := setup(t)
	for _, rel := range []string{"inside.txt", "./inside.txt", "sub/new.txt", "a/b/../c.txt"} {
		if _, err := worktree.Resolve(wt, rel); err != nil {
			t.Errorf("%q should resolve inside: %v", rel, err)
		}
	}
}

func TestResolveRefusesEscapes(t *testing.T) {
	wt, outside := setup(t)
	cases := map[string]string{
		"parent":      "../escape.txt",
		"deep parent": "../../../etc/passwd",
		"absolute":    filepath.Join(outside, "secret.txt"),
		"embedded":    "sub/../../outside.txt",
		"empty":       "",
	}
	for name, rel := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := worktree.Resolve(wt, rel); err == nil {
				t.Fatalf("%q was accepted", rel)
			} else if !errors.Is(err, worktree.ErrOutside) {
				t.Errorf("expected ErrOutside, got %v", err)
			}
		})
	}
}

// A symlink planted inside the worktree must not become an escape hatch:
// the cleaned path looks fine, and only resolving it reveals the escape.
func TestResolveFollowsSymlinksBeforeDeciding(t *testing.T) {
	wt, outside := setup(t)
	if err := os.Symlink(outside, filepath.Join(wt, "link")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	if _, err := worktree.Resolve(wt, "link/secret.txt"); err == nil {
		t.Fatal("a symlink to outside the worktree was accepted")
	}
	// Reading through it must fail too, not just resolving.
	if body, err := worktree.ReadWithin(wt, "link/secret.txt"); err == nil {
		t.Fatalf("read through a symlink escape returned %q", body)
	}
	// And writing through it must not create a file outside.
	if err := worktree.WriteWithin(wt, "link/planted.txt", []byte("x")); err == nil {
		t.Fatal("write through a symlink escape was accepted")
	}
	if _, err := os.Stat(filepath.Join(outside, "planted.txt")); err == nil {
		t.Fatal("a write escaped the worktree")
	}
}

// A symlink whose target is inside the worktree is fine: confinement is about
// where the path lands, not about whether a link was involved.
func TestSymlinksWithinTheWorktreeAreAllowed(t *testing.T) {
	wt, _ := setup(t)
	if err := os.Mkdir(filepath.Join(wt, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(wt, "real"), filepath.Join(wt, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := worktree.WriteWithin(wt, "alias/file.txt", []byte("ok")); err != nil {
		t.Fatalf("a symlink pointing inside the worktree should be allowed: %v", err)
	}
	body, err := worktree.ReadWithin(wt, "real/file.txt")
	if err != nil || string(body) != "ok" {
		t.Fatalf("read back %q, %v", body, err)
	}
}

func TestWriteWithinCreatesParents(t *testing.T) {
	wt, _ := setup(t)
	if err := worktree.WriteWithin(wt, "a/b/c/deep.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	body, err := worktree.ReadWithin(wt, "a/b/c/deep.txt")
	if err != nil || string(body) != "hi" {
		t.Fatalf("read back %q, %v", body, err)
	}
}

func TestListWithinIsConfined(t *testing.T) {
	wt, outside := setup(t)
	if _, err := worktree.ListWithin(wt, ".."); err == nil {
		t.Fatal("listing the parent was accepted")
	}
	if _, err := worktree.ListWithin(wt, outside); err == nil {
		t.Fatal("listing an absolute path outside was accepted")
	}
	entries, err := worktree.ListWithin(wt, ".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if !strings.Contains(strings.Join(names, " "), "inside.txt") {
		t.Errorf("listing the worktree root returned %v", names)
	}
}
