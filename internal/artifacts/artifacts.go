// Package artifacts is the content-addressed artifact store of design v3 §2.2:
// test output, diffs and screenshots under
// `workspaces/<id>/artifacts/<sha256>`.
//
// It is one of the two packages (with internal/store) permitted to write files
// directly; the storescope analyzer enforces that everywhere else.
package artifacts

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// ErrNotFound is returned when a hash is not in the store.
var ErrNotFound = errors.New("artifacts: not found")

// Store holds one workspace's artifacts.
type Store struct {
	ws  workspace.ID
	dir string
}

// New binds an artifact store to a workspace.
func New(s *store.Store) *Store { return &Store{ws: s.ID(), dir: s.ArtifactsDir()} }

// WorkspaceID reports the owning workspace.
func (s *Store) WorkspaceID() workspace.ID { return s.ws }

func (s *Store) path(hash string) string {
	// Two-level fan-out, as in the cache.
	return filepath.Join(s.dir, hash[:2], hash[2:])
}

// Put stores bytes and returns their sha256. Writing the same content twice is
// free: the second write sees the object already present.
func (s *Store) Put(body []byte) (string, error) {
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	p := s.path(hash)
	if _, err := os.Stat(p); err == nil {
		return hash, nil
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p), ".tmp-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	// Artifacts are immutable evidence; read-only permissions make an
	// accidental overwrite fail loudly.
	if err := os.Chmod(tmp.Name(), 0o440); err != nil {
		return "", err
	}
	return hash, os.Rename(tmp.Name(), p)
}

// PutReader streams content into the store.
func (s *Store) PutReader(r io.Reader) (string, error) {
	body, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return s.Put(body)
}

// Get returns the stored bytes for a hash.
func (s *Store) Get(hash string) ([]byte, error) {
	if len(hash) < 3 {
		return nil, fmt.Errorf("artifacts: %q is not a content hash", hash)
	}
	body, err := os.ReadFile(s.path(hash))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("%w: %s", ErrNotFound, hash)
		}
		return nil, err
	}
	// Verify on read: a corrupted artifact must not be served as evidence.
	sum := sha256.Sum256(body)
	if got := hex.EncodeToString(sum[:]); got != hash {
		return nil, fmt.Errorf("artifacts: %s is corrupt (content hashes to %s)", hash, got)
	}
	return body, nil
}

// Has reports whether a hash is present, without reading it.
func (s *Store) Has(hash string) bool {
	if len(hash) < 3 {
		return false
	}
	_, err := os.Stat(s.path(hash))
	return err == nil
}

// Size reports the total bytes held, for `le doctor`.
func (s *Store) Size() (int64, int, error) {
	var total int64
	var count int
	err := filepath.Walk(s.dir, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			total += info.Size()
			count++
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	}
	return total, count, err
}
