package task

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/recipe"
	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
	"github.com/akynte/local-engineer/internal/worktree"
)

// TestVerificationSandboxUsesSharedModuleCache verifies the sandbox spec that
// a task's verification runs under — the spec Runner.specFor produces —
// satisfies the invariants from design v3 §5.2:
//
// ONE: GOMODCACHE points to the provisioning module cache, not a path under
// the workspace directory.
//
// TWO: that same GOMODCACHE path appears in the spec's ReadWrite set, because
// a spec that names a path it does not grant compiles and tests clean and then
// fails as a permission error from inside the compiler.
//
// THREE: GOPROXY is still set to off, because a task must not reach the
// network and a future edit must not quietly remove that.
//
// FOUR: GOCACHE is still under the workspace directory, because build output
// is derived from one project's source and sharing it is not what this change
// did.
func TestVerificationSandboxUsesSharedModuleCache(t *testing.T) {
	tmpDir := t.TempDir()
	root, err := store.OpenRoot(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.CloseAll()

	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/task/test", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	r, err := NewRunner(st, nil, sandbox.ContainerRunner{}, "test-instance")
	if err != nil {
		t.Fatal(err)
	}
	r.SandboxSpec = sandbox.Spec{
		ReadOnly: []string{"/usr", "/bin", "/lib", "/lib64", "/etc"},
		TmpDir:   filepath.Join(tmpDir, "tmp"),
		Env: recipe.GoEnv(
			r.dirs.GoBuildCache,
			r.dirs.GoModCache,
			filepath.Join(tmpDir, "tmp")),
	}

	// Create a minimal worktree for specFor to consume.
	wtPath := filepath.Join(tmpDir, "worktree")
	if err := mkdirAll(wtPath); err != nil {
		t.Fatal(err)
	}
	wt := &worktree.Worktree{Path: wtPath}

	spec := r.specFor(wt)

	// ONE: GOMODCACHE must be the provisioning module cache path, not a path
	// under the workspace directory.
	gomodcache := envValue(spec.Env, "GOMODCACHE")
	if gomodcache == "" {
		t.Fatal("GOMODCACHE is not set in the sandbox spec")
	}
	if !strings.Contains(gomodcache, "provisioning") && !strings.Contains(gomodcache, "deps") {
		// The provisioning cache lives under deps/… in the store layout.
		// If it doesn't contain "provisioning" or "deps", it's likely a
		// workspace-local path — the bug we're guarding against.
		t.Fatalf("GOMODCACHE = %q; it must be the shared provisioning module cache, not a path under the workspace directory", gomodcache)
	}
	// The value must not contain the workspace id.
	workspaceID := st.ID()
	if strings.Contains(gomodcache, string(workspaceID)) {
		t.Fatalf("GOMODCACHE = %q; it must not contain the workspace id %q — the provisioning cache is shared across workspaces",
			gomodcache, workspaceID)
	}

	// TWO: the GOMODCACHE path must appear in the spec's ReadWrite set.
	foundRW := false
	for _, p := range spec.ReadWrite {
		if p == gomodcache {
			foundRW = true
			break
		}
	}
	if !foundRW {
		t.Fatalf("GOMODCACHE path %q does not appear in the spec's ReadWrite set; "+
			"a spec that names a path it does not grant compiles and tests clean "+
			"and then fails as a permission error from inside the compiler", gomodcache)
	}

	// THREE: GOPROXY must still be set to off.
	goproxy := envValue(spec.Env, "GOPROXY")
	if goproxy != "off" {
		t.Fatalf("GOPROXY = %q; a task must not reach the network and a future "+
			"edit must not quietly remove that", goproxy)
	}

	// FOUR: GOCACHE must be under the workspace directory.
	gocache := envValue(spec.Env, "GOCACHE")
	if gocache == "" {
		t.Fatal("GOCACHE is not set in the sandbox spec")
	}
	workspaceDir := st.Dir()
	if !strings.HasPrefix(gocache, workspaceDir) {
		t.Fatalf("GOCACHE = %q; it must be under the workspace directory %q, "+
			"because build output is derived from one project's source and "+
			"sharing it is not what this change did", gocache, workspaceDir)
	}
}

func mkdirAll(p string) error {
	return os.MkdirAll(p, 0o755)
}
