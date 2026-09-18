package bwrap_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/sandbox"
	"github.com/akynte/local-engineer/internal/sandbox/bwrap"
)

// §9 makes no network the default, and the default is what a caller gets by
// saying nothing. A spec that had to opt in would leave every call site one
// forgotten field away from an egress path.
func TestTheDefaultSpecUnsharesTheNetwork(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bubblewrap is not installed: %v", err)
	}
	r := &bwrap.Runner{}
	dir := t.TempDir()

	cmd, err := r.Command(context.Background(), sandbox.Spec{ReadWrite: []string{dir}, Dir: dir}, "true")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(cmd.Args, " "), "--unshare-net") {
		t.Fatalf("a spec that said nothing about the network kept it: %v", cmd.Args)
	}

	// The editing session has to reach the model gateway, and says so.
	cmd, err = r.Command(context.Background(), sandbox.Spec{ReadWrite: []string{dir}, Dir: dir, Network: sandbox.NetworkHost}, "true")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(strings.Join(cmd.Args, " "), "--unshare-net") {
		t.Fatalf("an explicit network grant was ignored: %v", cmd.Args)
	}
}

// The guarantee table is what `le doctor` prints, so a layer that cannot
// enforce this must not be listed as if it could.
func TestOnlyBwrapClaimsNetworkIsolation(t *testing.T) {
	for _, g := range sandbox.Guarantees() {
		if !strings.Contains(g.Statement, "Cannot reach the network") {
			continue
		}
		if g.Landlock || g.Container {
			t.Fatal("a layer that cannot unshare the network namespace claims it can")
		}
		if !g.Bwrap {
			t.Fatal("the bubblewrap layer does unshare the network and should say so")
		}
		if !strings.Contains(g.Note, "Multipath TCP") {
			t.Fatalf("the note omits the Landlock limit it has to state: %q", g.Note)
		}
		return
	}
	t.Fatal("the guarantee table has no row about network isolation")
}

// A bind replaces whatever the namespace had at that path, including earlier
// binds beneath it. When the task's tmp went on last, a data directory under
// /tmp lost its worktree and bwrap could not chdir into a path the spec had
// granted — which is every verification on a host whose data directory lives
// under /tmp.
func TestTheTaskTmpIsMountedBeforeThePathsThatMayLiveUnderIt(t *testing.T) {
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skipf("bubblewrap is not installed: %v", err)
	}
	r := &bwrap.Runner{}
	spec := sandbox.Spec{
		ReadWrite: []string{"/tmp/data/worktree"},
		TmpDir:    "/tmp/data/tmp",
		Dir:       "/tmp/data/worktree",
	}
	cmd, err := r.Command(context.Background(), spec, "true")
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(cmd.Args, " ")
	tmpAt := strings.Index(joined, "--bind /tmp/data/tmp /tmp")
	workAt := strings.Index(joined, "--bind /tmp/data/worktree /tmp/data/worktree")
	if tmpAt < 0 || workAt < 0 {
		t.Fatalf("expected both binds: %s", joined)
	}
	if tmpAt > workAt {
		t.Fatal("the tmp bind comes after a path beneath it, which discards that path")
	}
}
