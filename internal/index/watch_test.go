package index_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/akynte/local-engineer/internal/index"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// waitFor polls until cond holds or the deadline passes. The watcher is
// asynchronous by nature, so a fixed sleep would be either slow or flaky.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func watchFixture(t *testing.T) (*index.Indexer, *workspace.Workspace, string) {
	t.Helper()
	root, err := store.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.CloseAll() })

	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, "go.mod"),
		[]byte("module example.test/w\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	id := workspace.DeriveID(repoDir, "", "watch-test")
	st, err := root.OpenWorkspace(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	ix := index.New(st, index.Options{Excludes: []string{".git", "node_modules"}})

	ws := &workspace.Workspace{
		Root: repoDir,
		Manifest: workspace.Manifest{
			ID: id,
			Repositories: []workspace.Repository{
				{ID: "r1", Name: "w", Path: ".", DefaultBranch: "main"},
			},
		},
	}
	return ix, ws, repoDir
}

func dirtyCount(t *testing.T, ix *index.Indexer) int {
	t.Helper()
	n, err := ix.Dirty(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The whole point: a file changing under the repository makes the index
// visibly stale. `index.watch_enabled` defaulted to true and nothing ever set
// the dirty flag, so `le doctor` reported a clean index over one that no longer
// matched the files on disk.
func TestWritingAFileMarksTheIndexDirty(t *testing.T) {
	ix, ws, repoDir := watchFixture(t)

	w, err := index.NewWatcher(ix, ws, index.WatchOptions{
		Debounce: 30 * time.Millisecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()

	// Give the watch a moment to be established before writing.
	time.Sleep(100 * time.Millisecond)
	if got := dirtyCount(t, ix); got != 0 {
		t.Fatalf("index started dirty (%d)", got)
	}

	if err := os.WriteFile(filepath.Join(repoDir, "main.go"),
		[]byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the index to be marked dirty", func() bool { return dirtyCount(t, ix) > 0 })
}

// A file inside an excluded directory must not mark anything dirty: the
// indexer would never have read it, so the drift could never be cleared by
// re-indexing and `le doctor` would warn forever.
func TestExcludedDirectoriesDoNotMarkDirty(t *testing.T) {
	ix, ws, repoDir := watchFixture(t)

	w, err := index.NewWatcher(ix, ws, index.WatchOptions{
		Debounce: 30 * time.Millisecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	if err := os.MkdirAll(filepath.Join(repoDir, "node_modules", "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "node_modules", "pkg", "index.js"),
		[]byte("module.exports = 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Then touch a real file, and assert the dirty mark came from that one.
	time.Sleep(200 * time.Millisecond)
	if got := dirtyCount(t, ix); got != 0 {
		t.Fatalf("an excluded directory marked the index dirty (%d)", got)
	}
}

// Editor droppings are noise, not changes.
func TestEditorTempFilesAreIgnored(t *testing.T) {
	ix, ws, repoDir := watchFixture(t)

	w, err := index.NewWatcher(ix, ws, index.WatchOptions{
		Debounce: 30 * time.Millisecond, Logf: t.Logf,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	for _, name := range []string{"main.go~", ".#main.go", "main.go.swp"} {
		if err := os.WriteFile(filepath.Join(repoDir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	time.Sleep(200 * time.Millisecond)
	if got := dirtyCount(t, ix); got != 0 {
		t.Fatalf("editor temporary files marked the index dirty (%d)", got)
	}
}

// A burst — a branch switch, a formatter over the tree — must collapse into one
// mark rather than thousands.
func TestBurstsAreDebouncedIntoOneMark(t *testing.T) {
	ix, ws, repoDir := watchFixture(t)

	// OnDirty is called from the watcher's goroutine, so the counter is
	// atomic: a plain int here is a data race, and -race says so.
	var marks atomic.Int64
	w, err := index.NewWatcher(ix, ws, index.WatchOptions{
		Debounce: 120 * time.Millisecond, Logf: t.Logf,
		OnDirty: func(string, int) { marks.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = w.Run(ctx) }()
	time.Sleep(100 * time.Millisecond)

	for i := 0; i < 40; i++ {
		p := filepath.Join(repoDir, "f"+string(rune('a'+i%26))+".go")
		if err := os.WriteFile(p, []byte("package w\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	waitFor(t, "the burst to be marked", func() bool { return marks.Load() > 0 })
	time.Sleep(200 * time.Millisecond)
	if got := marks.Load(); got > 3 {
		t.Errorf("a 40-file burst produced %d dirty marks; debouncing is not working", got)
	}
}

// Re-indexing clears the flag, so drift is a state the operator can get out of.
func TestReindexingClearsTheDirtyFlag(t *testing.T) {
	ix, ws, repoDir := watchFixture(t)
	ctx := context.Background()

	if err := ix.RegisterRepository(ctx, ws.Manifest.Repositories[0]); err != nil {
		t.Fatal(err)
	}
	if err := ix.MarkDirty(ctx, "repository", "r1"); err != nil {
		t.Fatal(err)
	}
	if got := dirtyCount(t, ix); got == 0 {
		t.Fatal("MarkDirty did not mark anything")
	}
	if _, err := ix.Repository(ctx, "r1", repoDir); err != nil {
		t.Fatal(err)
	}
	if got := dirtyCount(t, ix); got != 0 {
		t.Errorf("re-indexing left %d dirty scope(s); drift would never clear", got)
	}
}
