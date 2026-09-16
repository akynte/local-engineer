package index_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// §3.4 requires dirty scopes to be re-analysed before a step that needs the
// graph. The watcher marked them and nothing acted on the mark, so a task
// asked the graph about code as it was when it was last indexed — and `le
// doctor` said so in its fix text while nothing performed it.
func TestRefreshReAnalysesADirtyRepository(t *testing.T) {
	repo, ix, g := indexedRepo(t)
	ctx := context.Background()
	src := filepath.Join(repo, "late.go")

	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}
	// A change the index has not seen, exactly as an operator editing between
	// tasks produces.
	if err := os.WriteFile(src,
		[]byte("package demo\n\nfunc ArrivedLate() string { return \"l\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(ctx, "repository", "r1"); err != nil {
		t.Fatal(err)
	}
	if countNamed(t, g, "ArrivedLate") != 0 {
		t.Fatal("the fixture is wrong: the symbol is already indexed")
	}

	if err := ix.Refresh(ctx, repo); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	if countNamed(t, g, "ArrivedLate") == 0 {
		t.Error("a dirty repository was not re-analysed; the graph still describes the old code")
	}
	if n, err := ix.Dirty(ctx); err != nil || n != 0 {
		t.Errorf("the dirty mark survived the refresh (n=%d, err=%v): the next step would "+
			"re-analyse again for nothing", n, err)
	}
}

// A clean repository must cost one query, not a full re-analysis. §3.4 puts
// this before every step that needs the graph, so paying for it when nothing
// changed would tax every task.
func TestRefreshIsCheapWhenNothingIsDirty(t *testing.T) {
	repo, ix, _ := indexedRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "a.go"),
		[]byte("package demo\n\nfunc A() {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ix.Repository(ctx, "r1", repo); err != nil {
		t.Fatal(err)
	}

	n, err := ix.Dirty(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("a freshly indexed repository reports %d dirty scope(s)", n)
	}

	_, missesBefore := ix.CacheStats()
	if err := ix.Refresh(ctx, repo); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if _, misses := ix.CacheStats(); misses != missesBefore {
		t.Error("refreshing a clean repository re-ran the analyzers")
	}
}

// A repository that was never indexed is dirty by construction — MarkDirty
// inserts a placeholder row — and refreshing it must index it rather than
// fail on the missing manifest.
func TestRefreshHandlesANeverIndexedRepository(t *testing.T) {
	repo, ix, g := indexedRepo(t)
	ctx := context.Background()
	if err := os.WriteFile(filepath.Join(repo, "first.go"),
		[]byte("package demo\n\nfunc FirstEver() string { return \"f\" }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(ctx, "repository", "r1"); err != nil {
		t.Fatal(err)
	}

	if err := ix.Refresh(ctx, repo); err != nil {
		t.Fatalf("refresh on a never-indexed repository: %v", err)
	}
	if countNamed(t, g, "FirstEver") == 0 {
		t.Error("the repository was never indexed and the refresh did not index it")
	}
}
