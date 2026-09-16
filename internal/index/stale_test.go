package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/akynte/local-engineer/internal/analyzers/golang"
	"github.com/akynte/local-engineer/internal/graph"
	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// indexedRepo returns a repository directory, an indexer bound to a fresh
// workspace, and the graph to query.
func indexedRepo(t *testing.T) (repo string, ix *index.Indexer, g graph.Graph) {
	t.Helper()
	repo = t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "go.mod"),
		[]byte("module example.com/demo\n\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.CloseAll() })
	st, err := root.OpenWorkspace(context.Background(),
		workspace.DeriveID("/index/stale", "", t.Name()))
	if err != nil {
		t.Fatal(err)
	}

	// The Go analyzer is what produces symbol nodes and what the cache is for;
	// an indexer with none would make every assertion below vacuous.
	ix = index.New(st, index.Options{Analyzers: []index.Analyzer{golang.New()}})
	if err := ix.RegisterRepository(context.Background(),
		workspace.Repository{ID: "r1", Name: "demo", Path: ".", DefaultBranch: "main"}); err != nil {
		t.Fatal(err)
	}
	return repo, ix, graph.New(st)
}

func countNamed(t *testing.T, g graph.Graph, name string) int {
	t.Helper()
	nodes, err := g.NodesByName(context.Background(), name, nil, 50)
	if err != nil {
		t.Fatalf("find %s: %v", name, err)
	}
	return len(nodes)
}

// A full index must write the graph that is, not the graph that has ever been.
// Nodes were only ever upserted, so a symbol deleted from the source stayed in
// the graph forever: impact analysis would report a consumer that no longer
// exists, and retrieval could hand the model a slice of code nobody has. §3.3
// treats a missing edge as "not discovered", which is honest — a present node
// for deleted code asserts something false.
func TestASymbolDeletedFromTheSourceLeavesTheGraph(t *testing.T) {
	repo, ix, g := indexedRepo(t)
	ctx := context.Background()
	src := filepath.Join(repo, "probe.go")

	if err := os.WriteFile(src,
		[]byte("package demo\n\nfunc StaleProbe() string { return \"x\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatalf("index with the symbol: %v", err)
	}
	if countNamed(t, g, "StaleProbe") == 0 {
		t.Fatal("the fixture never reached the graph, so this proves nothing")
	}

	if err := os.Remove(src); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatalf("index after the deletion: %v", err)
	}

	if n := countNamed(t, g, "StaleProbe"); n != 0 {
		t.Errorf("the graph still holds %d node(s) for a symbol that was deleted", n)
	}
}

// Rebuilding the graph must not lose what is still there. A fix that clears
// stale nodes by clearing everything would pass the test above and destroy the
// index.
func TestRebuildingKeepsTheSymbolsThatRemain(t *testing.T) {
	repo, ix, g := indexedRepo(t)
	ctx := context.Background()

	if err := os.WriteFile(filepath.Join(repo, "keep.go"),
		[]byte("package demo\n\nfunc Keeper() string { return \"k\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	temp := filepath.Join(repo, "temp.go")
	if err := os.WriteFile(temp,
		[]byte("package demo\n\nfunc Temporary() string { return \"t\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}

	if err := os.Remove(temp); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}

	if n := countNamed(t, g, "Keeper"); n == 0 {
		t.Error("a surviving symbol was removed from the graph by the rebuild")
	}
	if n := countNamed(t, g, "Temporary"); n != 0 {
		t.Errorf("the deleted symbol is still present %d time(s)", n)
	}
}

// §2.2 gives each workspace a cache keyed by content manifest, and the
// analyzers are the expensive part of indexing — this repository's own Go
// analysis is about 1.5 seconds of package loading and type checking. A second
// index of unchanged files must reuse it.
func TestUnchangedFilesReuseTheAnalyzerCache(t *testing.T) {
	repo, ix, _ := indexedRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "cached.go"),
		[]byte("package demo\n\nfunc Cached() string { return \"c\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	_, missesAfterFirst := ix.CacheStats()
	if missesAfterFirst == 0 {
		t.Fatal("the first index hit the cache, so the fixture is not measuring anything")
	}

	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	hits, misses := ix.CacheStats()
	if hits == 0 {
		t.Error("indexing unchanged files re-derived every analyzer result")
	}
	if misses > missesAfterFirst {
		t.Errorf("unchanged files produced %d new miss(es): the key is not stable across runs",
			misses-missesAfterFirst)
	}
}

// The key has to move when the content does, or the cache serves a graph for
// code that is no longer there — which is a far worse failure than a slow
// index.
func TestChangedFilesMissTheCache(t *testing.T) {
	repo, ix, _ := indexedRepo(t)
	ctx := context.Background()
	src := filepath.Join(repo, "moving.go")

	if err := os.WriteFile(src,
		[]byte("package demo\n\nfunc First() string { return \"1\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	_, before := ix.CacheStats()

	if err := os.WriteFile(src,
		[]byte("package demo\n\nfunc Second() string { return \"2\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	if _, after := ix.CacheStats(); after == before {
		t.Error("changing a file's contents did not invalidate the cached analysis")
	}
}
