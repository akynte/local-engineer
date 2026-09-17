package session_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/session"
	"github.com/akynte/local-engineer/internal/workspace"
)

// repo creates a directory that is a Local Engineer workspace.
func repo(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module example.com/"+name+"\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := workspace.Init(dir, workspace.InitOptions{Name: name}); err != nil {
		t.Fatalf("workspace init: %v", err)
	}
	return dir
}

// Two repositories must resolve to two workspaces. A long-lived server binds to
// one, then another, then back; if any of that were cached or shared, one
// project would be answering questions about another's code.
func TestTwoRepositoriesBindToDifferentWorkspaces(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	a, b := repo(t, "alpha"), repo(t, "beta")

	sa, err := session.Open(ctx, data, a)
	if err != nil {
		t.Fatal(err)
	}
	idA := sa.Workspace.ID()
	if err := sa.Close(); err != nil {
		t.Fatal(err)
	}

	sb, err := session.Open(ctx, data, b)
	if err != nil {
		t.Fatal(err)
	}
	idB := sb.Workspace.ID()
	if err := sb.Close(); err != nil {
		t.Fatal(err)
	}

	if idA == idB {
		t.Fatalf("two repositories share the workspace id %s", idA)
	}
	if sa.Store.ID() == sb.Store.ID() {
		t.Error("two workspaces were given the same store identity")
	}
}

// §2.2 clears the previous workspace's saved inference slots on a switch. A CLI
// process switches once and exits, which makes the step easy to mistake for a
// formality; a server switches on every call. This asserts the switch happened.
func TestSwitchingWorkspacesIsRecorded(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	a, b := repo(t, "alpha"), repo(t, "beta")

	sa, err := session.Open(ctx, data, a)
	if err != nil {
		t.Fatal(err)
	}
	idA := sa.Workspace.ID()
	if got := sa.Root.Active(); got != idA {
		t.Errorf("after binding to alpha the active workspace is %s, want %s", got, idA)
	}
	sa.Close()

	sb, err := session.Open(ctx, data, b)
	if err != nil {
		t.Fatal(err)
	}
	defer sb.Close()
	if got := sb.Root.Active(); got != sb.Workspace.ID() {
		t.Errorf("after binding to beta the active workspace is %s, want %s",
			got, sb.Workspace.ID())
	}
	if sb.Root.Active() == idA {
		t.Error("binding to beta left alpha active: the §2.2 switch did not happen, " +
			"so beta would run against slots alpha warmed")
	}
}

// Re-binding to the same workspace must be ordinary. A server asked twice about
// one repository should not be doing anything special the second time.
func TestRebindingToTheSameWorkspaceIsStable(t *testing.T) {
	ctx := context.Background()
	data := t.TempDir()
	a := repo(t, "alpha")

	first, err := session.Open(ctx, data, a)
	if err != nil {
		t.Fatal(err)
	}
	id := first.Workspace.ID()
	first.Close()

	second, err := session.Open(ctx, data, a)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if second.Workspace.ID() != id {
		t.Errorf("the same repository resolved to %s then %s", id, second.Workspace.ID())
	}
}

// A directory that is not a workspace has to say so as its own error, so a
// caller can offer to initialise one rather than reporting a storage fault.
func TestADirectoryThatIsNotAWorkspaceIsTyped(t *testing.T) {
	_, err := session.Open(context.Background(), t.TempDir(), t.TempDir())
	if err == nil {
		t.Fatal("an uninitialised directory opened a session")
	}
	var missing *session.ErrNoWorkspace
	if !errors.As(err, &missing) {
		t.Errorf("error is %T, want *session.ErrNoWorkspace so callers can tell it apart", err)
	}
	if !errors.Is(err, workspace.ErrNotAWorkspace) {
		t.Error("the typed error must still unwrap to workspace.ErrNotAWorkspace")
	}
}
