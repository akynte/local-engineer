package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/akynte/local-engineer/internal/workspace"
)

// Watcher marks index scopes dirty when files change underneath them (§3.4).
//
// Everything downstream of this already existed: index_keys carries a dirty
// column, `le doctor` reports the count as index drift, and graph stats expose
// it. Nothing ever set it. `index.watch_enabled` defaulted to true and did
// nothing at all, which is worse than the feature being absent — the
// configuration said the index was being kept honest while it went stale in
// silence.
//
// What the watcher does not do is re-index. Re-analysis is the caller's
// decision because it costs real time and belongs before a step that needs the
// graph, not in the middle of an editor save. The watcher's whole job is to
// make staleness visible.
type Watcher struct {
	ix    *Indexer
	roots map[string]string // absolute path -> repository id

	// debounce collapses the burst of events an editor save or a git checkout
	// produces. Without it a branch switch marks the same scope dirty
	// thousands of times.
	debounce time.Duration

	mu      sync.Mutex
	pending map[string]bool
	w       *fsnotify.Watcher

	// OnDirty is called after a scope is marked, for logging and tests.
	OnDirty func(repositoryID string, paths int)
	// Logf reports non-fatal problems. A watch that cannot be established on
	// one directory must not stop the rest.
	Logf func(format string, args ...any)
}

// WatchOptions configures a watcher.
type WatchOptions struct {
	// Debounce is how long to wait for a burst to settle. Zero uses 250ms.
	Debounce time.Duration
	Logf     func(format string, args ...any)
	OnDirty  func(repositoryID string, paths int)
}

// NewWatcher builds a watcher over a workspace's repositories.
func NewWatcher(ix *Indexer, ws *workspace.Workspace, opts WatchOptions) (*Watcher, error) {
	if ix == nil || ws == nil {
		return nil, errors.New("index: a watcher needs an indexer and a workspace")
	}
	if opts.Debounce <= 0 {
		opts.Debounce = 250 * time.Millisecond
	}
	roots := map[string]string{}
	for _, repo := range ws.Manifest.Repositories {
		abs, err := filepath.Abs(filepath.Join(ws.Root, filepath.FromSlash(repo.Path)))
		if err != nil {
			return nil, err
		}
		roots[abs] = repo.ID
	}
	if len(roots) == 0 {
		return nil, errors.New("index: the workspace declares no repositories to watch")
	}
	return &Watcher{
		ix: ix, roots: roots, debounce: opts.Debounce,
		pending: map[string]bool{}, Logf: opts.Logf, OnDirty: opts.OnDirty,
	}, nil
}

func (w *Watcher) logf(format string, args ...any) {
	if w.Logf != nil {
		w.Logf(format, args...)
	}
}

// Run watches until the context is cancelled.
func (w *Watcher) Run(ctx context.Context) error {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("index: starting the file watcher: %w", err)
	}
	defer fsw.Close()

	w.mu.Lock()
	w.w = fsw
	w.mu.Unlock()

	for root := range w.roots {
		if err := w.addTree(fsw, root); err != nil {
			// One unreadable directory must not stop the watch. Saying so is
			// the honest alternative to silently covering less than claimed.
			w.logf("index: watching %s: %v", root, err)
		}
	}

	timer := time.NewTimer(w.debounce)
	if !timer.Stop() {
		<-timer.C
	}
	armed := false

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()

		case ev, ok := <-fsw.Events:
			if !ok {
				return nil
			}
			if w.ignored(ev.Name) {
				continue
			}
			// A new directory has to be watched too, or everything created
			// after start-up is invisible.
			if ev.Op&fsnotify.Create != 0 {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					if err := w.addTree(fsw, ev.Name); err != nil {
						w.logf("index: watching new directory %s: %v", ev.Name, err)
					}
				}
			}
			w.mu.Lock()
			w.pending[ev.Name] = true
			w.mu.Unlock()
			if !armed {
				timer.Reset(w.debounce)
				armed = true
			}

		case err, ok := <-fsw.Errors:
			if !ok {
				return nil
			}
			w.logf("index: watcher: %v", err)

		case <-timer.C:
			armed = false
			if err := w.flush(ctx); err != nil {
				w.logf("index: marking dirty: %v", err)
			}
		}
	}
}

// flush marks every repository with pending changes dirty, once.
func (w *Watcher) flush(ctx context.Context) error {
	w.mu.Lock()
	paths := make([]string, 0, len(w.pending))
	for p := range w.pending {
		paths = append(paths, p)
	}
	w.pending = map[string]bool{}
	w.mu.Unlock()

	byRepo := map[string]int{}
	for _, p := range paths {
		if id, ok := w.repositoryFor(p); ok {
			byRepo[id]++
		}
	}
	var errs []error
	for id, n := range byRepo {
		if err := w.ix.MarkDirty(ctx, "repository", id); err != nil {
			errs = append(errs, err)
			continue
		}
		if w.OnDirty != nil {
			w.OnDirty(id, n)
		}
	}
	return errors.Join(errs...)
}

// repositoryFor finds which repository a path belongs to, preferring the
// longest matching root so a nested repository wins over its parent.
func (w *Watcher) repositoryFor(path string) (string, bool) {
	best, bestID := "", ""
	for root, id := range w.roots {
		if root == path || strings.HasPrefix(path, root+string(os.PathSeparator)) {
			if len(root) > len(best) {
				best, bestID = root, id
			}
		}
	}
	return bestID, bestID != ""
}

// addTree watches a directory and everything under it that is not excluded.
func (w *Watcher) addTree(fsw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			// A directory that cannot be read is skipped, not fatal.
			return nil //nolint:nilerr // see above
		}
		if !d.IsDir() {
			return nil
		}
		if p != root && w.excludedDir(filepath.Base(p)) {
			return filepath.SkipDir
		}
		return fsw.Add(p)
	})
}

// ignored reports whether an event is noise. The exclude list is the indexer's
// own, so the watcher never marks dirty for a file the indexer would not have
// read — which would produce drift that re-indexing could never clear.
func (w *Watcher) ignored(path string) bool {
	base := filepath.Base(path)
	// Editor and tool droppings.
	switch {
	case strings.HasSuffix(base, "~"),
		strings.HasPrefix(base, ".#"),
		strings.HasSuffix(base, ".swp"),
		strings.HasSuffix(base, ".swx"),
		strings.HasSuffix(base, ".tmp"),
		strings.Contains(base, ".goutputstream"):
		return true
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if w.excludedDir(part) {
			return true
		}
	}
	return false
}

func (w *Watcher) excludedDir(name string) bool {
	for _, ex := range w.ix.opts.Excludes {
		if name == ex {
			return true
		}
	}
	return false
}
