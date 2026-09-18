package native_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/engine"
	"github.com/akynte/local-engineer/internal/firewall"
	"github.com/akynte/local-engineer/internal/ledger"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

func TestToolDecisionsAreJournalledAndJournalFailurePreventsWrites(t *testing.T) {
	ctx := context.Background()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(ctx, workspace.DeriveID("/native/journal", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	journal := ledger.New(st)
	wt := worktree(t, map[string]string{"a.go": "old"})
	p := newScripted(
		call("denied", "write_file", map[string]any{"path": "other.go", "content": "bad"}),
		call("allowed", "edit_file", map[string]any{"path": "a.go", "old": "old", "new": "new"}),
	)
	req := engine.Request{TaskID: "journal-test", Worktree: wt, Journal: journal, Access: firewall.Access{WriteScope: []string{"a.go"}}}
	if _, err := newEngine(t, p).Step(ctx, req); err != nil {
		t.Fatal(err)
	}
	ops, err := journal.Operations(ctx, req.TaskID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ops) != 2 || ops[0].Uncertain() || ops[1].Uncertain() ||
		!strings.Contains(string(ops[0].Intent), `"decision":"deny"`) ||
		!strings.Contains(string(ops[1].Intent), `"decision":"allow"`) {
		t.Fatalf("missing decision/outcome: %+v", ops)
	}
	if err := root.CloseAll(); err != nil {
		t.Fatal(err)
	}
	p = newScripted(call("no-journal", "edit_file", map[string]any{"path": "a.go", "old": "new", "new": "unrecorded"}))
	if _, err := newEngine(t, p).Step(ctx, req); err == nil {
		t.Fatal("write proceeded without journal")
	}
	body, err := os.ReadFile(filepath.Join(wt, "a.go"))
	if err != nil || string(body) != "new" {
		t.Fatalf("unrecorded side effect: %q %v", body, err)
	}
}
