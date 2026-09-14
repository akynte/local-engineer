package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/akynte/local-engineer/internal/version"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Root is the opened data directory. It is the only object that can produce a
// Store, and it can only produce one bound to a specific workspace id.
type Root struct {
	layout *Layout

	mu   sync.Mutex
	open map[workspace.ID]*Store
}

// OpenRoot opens the data directory at dir (or $LE_DATA, or /data).
func OpenRoot(dir string) (*Root, error) {
	l, err := NewLayout(dir)
	if err != nil {
		return nil, err
	}
	return &Root{layout: l, open: map[workspace.ID]*Store{}}, nil
}

// Layout exposes path resolution for callers that need to place sandbox mounts
// and child-process XDG directories (§2.2). It never exposes a database.
func (r *Root) Layout() *Layout { return r.layout }

// Record is the data-side note of which repository a workspace id belongs to.
// It is what `le workspace list` reads; the authoritative pin lives in the
// repository's `.le/workspace.yaml`.
type Record struct {
	ID         workspace.ID `json:"id"`
	Name       string       `json:"name"`
	Root       string       `json:"root"`
	LastOpened time.Time    `json:"last_opened"`
	Scheme     int          `json:"scheme_version"`
}

// Store is a handle on exactly one workspace's persistent state. Every query
// in the system goes through one of these; there is no API that reaches a
// table without a workspace handle (§2.3).
type Store struct {
	id     workspace.ID
	layout *Layout
	root   *Root

	index     *DB
	ledger    *DB
	telemetry *DB

	closeOnce sync.Once
}

// OpenWorkspace opens (creating and migrating if needed) the three databases
// of one workspace. This is the storage API named in §2.3:
//
//	OpenWorkspace(id) -> Store
//
// Repeated calls for the same id return the same handle so that a process
// never holds two independent writers on one ledger.
func (r *Root) OpenWorkspace(ctx context.Context, id workspace.ID) (*Store, error) {
	if !id.Valid() {
		return nil, fmt.Errorf("store: %q is not a valid workspace id", id)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if s, ok := r.open[id]; ok {
		return s, nil
	}

	for _, d := range r.layout.workspaceSubdirs(id) {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", d, err)
		}
	}

	s := &Store{id: id, layout: r.layout, root: r}
	specs := []struct {
		name string
		path string
		dur  Durability
		dst  **DB
	}{
		{"index", r.layout.IndexDB(id), DurabilityNormal, &s.index},
		{"ledger", r.layout.LedgerDB(id), DurabilityFull, &s.ledger},
		{"telemetry", r.layout.TelemetryDB(id), DurabilityNormal, &s.telemetry},
	}
	for _, spec := range specs {
		db, err := openDB(ctx, id, spec.name, spec.path, spec.dur)
		if err != nil {
			// Report the cleanup failure alongside the cause: a database that
			// would not close may have left a write-ahead log behind.
			return nil, errors.Join(err, s.closeAll())
		}
		if err := db.migrate(ctx, id); err != nil {
			return nil, errors.Join(err, db.Close(), s.closeAll())
		}
		*spec.dst = db
	}

	r.open[id] = s
	return s, nil
}

// ID reports the workspace this store is bound to.
func (s *Store) ID() workspace.ID { return s.id }

// Index, Ledger and Telemetry return the three per-workspace databases (§5.2:
// three files so that a large index rebuild never blocks the ledger, and the
// ledger can be backed up independently at high frequency).
func (s *Store) Index() *DB     { return s.index }
func (s *Store) Ledger() *DB    { return s.ledger }
func (s *Store) Telemetry() *DB { return s.telemetry }

// Directory accessors, all rooted at workspaces/<id>/ (§2.2).
func (s *Store) Dir() string          { return s.layout.WorkspaceDir(s.id) }
func (s *Store) ArtifactsDir() string { return s.layout.ArtifactsDir(s.id) }
func (s *Store) CacheDir() string     { return s.layout.CacheDir(s.id) }
func (s *Store) OpenCodeDir() string  { return s.layout.OpenCodeDir(s.id) }
func (s *Store) SlotsDir() string     { return s.layout.SlotsDir(s.id) }
func (s *Store) TmpDir() string       { return s.layout.TmpDir(s.id) }

// DBs returns all three handles, for maintenance operations that must treat
// them uniformly (doctor, backup, shutdown).
func (s *Store) DBs() []*DB { return []*DB{s.index, s.ledger, s.telemetry} }

// RecordWorkspace writes the data-side note for `le workspace list`.
func (s *Store) RecordWorkspace(ws *workspace.Workspace) error {
	rec := Record{
		ID:         ws.ID(),
		Name:       ws.Name(),
		Root:       ws.Root,
		LastOpened: time.Now().UTC().Truncate(time.Second),
		Scheme:     ws.Manifest.SchemeVersion,
	}
	body, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	path := s.layout.RecordPath(s.id)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearSlots removes llama-server saved slots. §2.2 requires this on every
// workspace switch so that neither cache contents nor cache timing can leak
// between projects.
func (s *Store) ClearSlots() error {
	return clearDir(s.SlotsDir())
}

// ClearTmp wipes the per-workspace tmp directory, done on task end (§2.2).
func (s *Store) ClearTmp() error {
	return clearDir(s.TmpDir())
}

func clearDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	for _, e := range entries {
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// Checkpoint flushes every WAL, called on orderly shutdown (§4.4).
func (s *Store) Checkpoint(ctx context.Context) error {
	var errs []error
	for _, db := range s.DBs() {
		if db != nil {
			errs = append(errs, db.Checkpoint(ctx))
		}
	}
	return errors.Join(errs...)
}

// Close flushes and closes all three databases.
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		err = errors.Join(s.Checkpoint(ctx), s.closeAll())
		if s.root != nil {
			s.root.mu.Lock()
			delete(s.root.open, s.id)
			s.root.mu.Unlock()
		}
	})
	return err
}

func (s *Store) closeAll() error {
	var errs []error
	for _, db := range []*DB{s.index, s.ledger, s.telemetry} {
		if db != nil {
			errs = append(errs, db.Close())
		}
	}
	return errors.Join(errs...)
}

// ListWorkspaces enumerates the data-side records. It reads only
// workspaces/<id>/workspace.json and never opens a database, so listing costs
// nothing and cannot deadlock against a running supervisor.
func (r *Root) ListWorkspaces() ([]Record, error) {
	entries, err := os.ReadDir(r.layout.WorkspacesDir())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []Record
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := workspace.ID(e.Name())
		if !id.Valid() {
			continue
		}
		body, err := os.ReadFile(r.layout.RecordPath(id))
		if err != nil {
			out = append(out, Record{ID: id, Name: "(no record)", Scheme: version.WorkspaceIDScheme})
			continue
		}
		var rec Record
		if err := json.Unmarshal(body, &rec); err != nil {
			out = append(out, Record{ID: id, Name: "(unreadable record)"})
			continue
		}
		rec.ID = id
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CloseAll closes every open workspace store.
func (r *Root) CloseAll() error {
	r.mu.Lock()
	stores := make([]*Store, 0, len(r.open))
	for _, s := range r.open {
		stores = append(stores, s)
	}
	r.mu.Unlock()
	var errs []error
	for _, s := range stores {
		errs = append(errs, s.Close())
	}
	return errors.Join(errs...)
}

// Backup writes a consistent snapshot of all three databases into dir (§4.4).
func (s *Store) Backup(ctx context.Context, dir string) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	for _, db := range s.DBs() {
		dst := filepath.Join(dir, db.Name()+".db")
		if err := os.RemoveAll(dst); err != nil {
			return err
		}
		if err := db.Backup(ctx, dst); err != nil {
			return err
		}
	}
	return nil
}
