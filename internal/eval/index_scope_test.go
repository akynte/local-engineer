package eval_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/golang"
	"github.com/akynte/local-engineer/internal/eval"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/retrieval"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// TestSupervisedArmIndexesTheTaskCopy is the fix for an evaluation that could
// not have measured what it claimed to.
//
// The supervised arms exist to measure retrieval and the graph. Both were
// wired to the operator's own workspace store, and nothing indexed the task
// copy — so search_code and find_symbol answered from a different repository,
// or from an empty index when the operator had never run `le index`. Every
// comparison against those arms would have shown the graph contributing
// nothing, for a reason that has nothing to do with the graph.
//
// The test asserts the property directly: after a run, a retriever built on
// the run's own workspace finds a symbol that exists only in the fixture.
func TestSupervisedArmIndexesTheTaskCopy(t *testing.T) {
	requireGo(t)

	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.CloseAll() })

	// A symbol that appears nowhere but this fixture, so a hit cannot have
	// come from anything the operator indexed.
	const marker = "ZzUniqueFixtureSymbol"
	dir := t.TempDir()
	fixture := filepath.Join(dir, "fixture")
	write(t, filepath.Join(fixture, "go.mod"), "module example.test/scope\n\ngo 1.26\n")
	write(t, filepath.Join(fixture, "calc.go"),
		"package calc\n\nfunc "+marker+"() int { return 1 }\n")

	work := fixture

	id := workspace.DeriveID(work, "", "eval-scope-001-supervised")
	st, err := root.OpenWorkspace(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}

	ix := index.New(st, index.Options{Analyzers: []index.Analyzer{golang.New()}})
	repo := workspace.Repository{
		ID: workspace.DeriveRepositoryID(id, ".", ""), Name: "scope", Path: ".", DefaultBranch: "main",
	}
	if err := ix.RegisterRepository(context.Background(), repo); err != nil {
		t.Fatal(err)
	}
	stats, err := ix.Repository(context.Background(), repo.ID, work)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Files == 0 {
		t.Fatal("indexing the task copy produced no files; a supervised arm would " +
			"retrieve from an empty index")
	}

	// The property under test: the run's index answers about the task copy.
	pkt, err := retrieval.New(st).Build(context.Background(), retrieval.Request{
		Query: marker, MaxAnchors: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, sl := range pkt.Slices {
		if strings.Contains(sl.Symbol, marker) || strings.Contains(sl.Signature, marker) ||
			strings.Contains(sl.Body, marker) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("retrieval returned %d slice(s), none mentioning %s — a symbol that "+
			"exists only in the task copy. The supervised arm is not retrieving from "+
			"the fixture.", len(pkt.Slices), marker)
	}
	// And the packet must be scoped to this run's workspace, not the operator's.
	if pkt.WorkspaceID != id {
		t.Errorf("packet workspace = %s, want the run's own workspace %s", pkt.WorkspaceID, id)
	}
	if len(pkt.Rejected) > 0 {
		t.Errorf("slices were rejected by the isolation guard: %v", pkt.Rejected)
	}
}

// TestSupervisedArmRefusesWithoutAnIndex pins the failure as loud. A supervised
// arm with nowhere to build an index must stop the run and say so, rather than
// finish and report a zero that reads as the pipeline failing to solve the
// task. The check runs before the provider lookup so a misconfiguration costs
// nothing.
func TestSupervisedArmRefusesWithoutAnIndex(t *testing.T) {
	s := &eval.SystemSolver{} // no Root, and no Router either
	_, err := s.Solve(context.Background(), eval.SolveRequest{
		Task: eval.Task{ID: "t"},
		Arm:  eval.Arm{Name: "supervised", Supervised: true},
	})
	if err == nil {
		t.Fatal("the supervised arm was allowed to run with no index to retrieve from")
	}
	if !strings.Contains(err.Error(), "empty index") {
		t.Errorf("the error does not explain what would have gone wrong: %v", err)
	}
}
