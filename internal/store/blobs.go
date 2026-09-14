package store

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// BlobDir is a namespaced byte store inside a workspace's cache directory.
//
// It exists so that no package outside internal/store and internal/artifacts
// needs to write files: §2.3 forbids that, and the storescope analyzer
// enforces it. Callers derive keys and this type owns the I/O.
type BlobDir struct {
	ws  string
	dir string
}

// Blobs returns a namespaced blob directory under workspaces/<id>/cache/.
func (s *Store) Blobs(namespace string) (*BlobDir, error) {
	if namespace == "" || filepath.Clean(namespace) != namespace || filepath.IsAbs(namespace) {
		return nil, fmt.Errorf("store: %q is not a valid blob namespace", namespace)
	}
	dir := filepath.Join(s.CacheDir(), namespace)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, err
	}
	return &BlobDir{ws: s.id.String(), dir: dir}, nil
}

// ErrNoBlob is returned when a key is absent.
var ErrNoBlob = errors.New("store: blob not found")

func (b *BlobDir) path(key string) (string, error) {
	if len(key) < 3 || filepath.Clean(key) != key || filepath.IsAbs(key) ||
		filepath.Base(key) != key {
		return "", fmt.Errorf("store: %q is not a valid blob key", key)
	}
	return filepath.Join(b.dir, key[:2], key[2:]), nil
}

// Get reads a blob.
func (b *BlobDir) Get(key string) ([]byte, error) {
	p, err := b.path(key)
	if err != nil {
		return nil, err
	}
	body, err := os.ReadFile(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoBlob
		}
		return nil, err
	}
	return body, nil
}

// Put writes a blob atomically.
func (b *BlobDir) Put(key string, body []byte) error {
	p, err := b.path(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p)
}

// Has reports presence without reading.
func (b *BlobDir) Has(key string) bool {
	p, err := b.path(key)
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Dir reports the namespace's directory, for diagnostics only.
func (b *BlobDir) Dir() string { return b.dir }

// Clear removes every blob in the namespace.
func (b *BlobDir) Clear() error { return clearDir(b.dir) }

// Size reports bytes and entries held.
func (b *BlobDir) Size() (int64, int, error) {
	var total int64
	var count int
	err := filepath.WalkDir(b.dir, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		count++
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	return total, count, err
}

// RestoreFrom copies backed-up database files back into the workspace
// directory. The store owns this because it is a data-directory write and
// because the WAL and shm sidecars must be removed with it: a restored
// database paired with the previous write-ahead log is silent corruption.
//
// The caller must Close the store first; the restored files are validated by
// re-opening the workspace.
func (s *Store) RestoreFrom(dir string, force bool) ([]string, error) {
	// Resolve the source once, and require it to be a real directory. The
	// destination is always this workspace's own directory joined with a name
	// from the fixed list below, so a crafted source path can influence what is
	// read but never where it is written.
	srcDir, err := filepath.Abs(filepath.Clean(dir))
	if err != nil {
		return nil, fmt.Errorf("store: resolve backup directory: %w", err)
	}
	if st, err := os.Stat(srcDir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("store: %s is not a readable backup directory", srcDir)
	}

	var restored []string
	for _, name := range []string{"index.db", "ledger.db", "telemetry.db"} {
		src := filepath.Join(srcDir, name)
		body, err := os.ReadFile(src) //nolint:gosec // srcDir is validated above; name is from the fixed list
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue // a backup may legitimately hold a subset
			}
			return restored, err
		}
		dst := filepath.Join(s.Dir(), name)
		if _, err := os.Stat(dst); err == nil && !force {
			return restored, fmt.Errorf("store: %s already exists; pass --force to overwrite", dst)
		}
		for _, sidecar := range []string{dst + "-wal", dst + "-shm"} {
			if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
				return restored, err
			}
		}
		// dst is this workspace's own directory joined with a name from the
		// fixed list above; nothing from the caller reaches the write path.
		if err := os.WriteFile(dst, body, 0o640); err != nil { //nolint:gosec // see above
			return restored, err
		}
		restored = append(restored, name)
	}
	if len(restored) == 0 {
		return nil, fmt.Errorf("store: %s holds none of index.db, ledger.db, telemetry.db", srcDir)
	}
	return restored, nil
}

// TaskDirs are the per-workspace directories a task run needs. They are
// created here rather than by the task runner because §2.3 confines directory
// creation to this package: the rule is what keeps every path a subsystem
// touches one the store chose.
type TaskDirs struct {
	// Worktrees is where per-task checkouts live.
	Worktrees string
	// GoBuildCache and GoModCache are per-workspace, so one project's build
	// cache never serves another's.
	GoBuildCache string
	GoModCache   string
	// Tmp is the task's only visible temporary directory.
	Tmp string
}

// TaskDirs returns the per-task directories, creating them.
func (s *Store) TaskDirs() (TaskDirs, error) {
	d := TaskDirs{
		Worktrees:    filepath.Join(s.Dir(), "worktrees"),
		GoBuildCache: filepath.Join(s.CacheDir(), "go-build"),
		GoModCache:   filepath.Join(s.CacheDir(), "go-mod"),
		Tmp:          s.TmpDir(),
	}
	for _, p := range []string{d.Worktrees, d.GoBuildCache, d.GoModCache, d.Tmp} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return d, fmt.Errorf("store: create %s: %w", p, err)
		}
	}
	return d, nil
}

// EnsureSandboxDirs creates the writable paths a sandbox spec names, so a
// Landlock rule is never dropped for a missing directory — which would fail
// the task on a write the sandbox intended to allow.
//
// Only paths inside this workspace are created; anything else is left alone,
// so a caller cannot use this to reach outside its own directory.
func (s *Store) EnsureSandboxDirs(paths []string) error {
	root := s.Dir()
	for _, p := range paths {
		if p == "" || !strings.HasPrefix(filepath.Clean(p), root) {
			continue
		}
		if err := os.MkdirAll(p, 0o750); err != nil {
			return fmt.Errorf("store: create %s: %w", p, err)
		}
	}
	return nil
}
