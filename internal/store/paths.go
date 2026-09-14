// Package store is the single entry point for all persistence (design v3 §13).
//
// The isolation contract of §2.2 is enforced here rather than by convention:
// the only way to reach a database is OpenWorkspace, which returns a handle
// already bound to one workspace's files. There is no exported API that takes
// a bare path or a table name. A custom analyzer (tools/analyzers/storescope)
// fails the build if `sql.Open` or direct file writes appear outside this
// package and internal/artifacts (§2.3).
package store

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/akynte/local-engineer/internal/workspace"
)

// EnvDataDir names the environment variable holding the data root. Inside the
// container this is /data, backed by the persistent volume (§4.1).
const EnvDataDir = "LE_DATA"

// DefaultDataDir is used when LE_DATA is unset.
const DefaultDataDir = "/data"

// Layout resolves every path under the data root. Nothing outside these
// functions may compose a path into a workspace directory.
type Layout struct{ root string }

// NewLayout binds a layout to a data root, creating the top-level directories.
func NewLayout(root string) (*Layout, error) {
	if root == "" {
		root = os.Getenv(EnvDataDir)
	}
	if root == "" {
		root = DefaultDataDir
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("store: resolve data dir: %w", err)
	}
	l := &Layout{root: abs}
	for _, d := range []string{l.Root(), l.ConfigDir(), l.ModelsDir(), l.WorkspacesDir(), l.BackupsDir()} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			return nil, fmt.Errorf("store: create %s: %w", d, err)
		}
	}
	return l, nil
}

func (l *Layout) Root() string          { return l.root }
func (l *Layout) ConfigDir() string     { return filepath.Join(l.root, "config") }
func (l *Layout) ModelsDir() string     { return filepath.Join(l.root, "models") }
func (l *Layout) WorkspacesDir() string { return filepath.Join(l.root, "workspaces") }
func (l *Layout) BackupsDir() string    { return filepath.Join(l.root, "backups") }

// WorkspaceDir is `$LE_DATA/workspaces/<workspace_id>` — the only directory a
// workspace's state may occupy (§2.2).
func (l *Layout) WorkspaceDir(id workspace.ID) string {
	return filepath.Join(l.WorkspacesDir(), id.String())
}

// The per-workspace layout of §5.2.
func (l *Layout) IndexDB(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "index.db")
}
func (l *Layout) LedgerDB(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "ledger.db")
}
func (l *Layout) TelemetryDB(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "telemetry.db")
}
func (l *Layout) ArtifactsDir(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "artifacts")
}
func (l *Layout) CacheDir(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "cache")
}

// OpenCodeDir holds the engine's XDG directories so OpenCode never sees
// another workspace's data (§2.2).
func (l *Layout) OpenCodeDir(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "opencode")
}

// SlotsDir holds llama-server saved prompt-cache slots. Cleared on workspace
// switch so that timing and cache statistics cannot leak either (§2.2).
func (l *Layout) SlotsDir(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "slots")
}

// TmpDir is wiped on task end; sandboxed processes see only this tmp (§2.2).
func (l *Layout) TmpDir(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "tmp")
}

// RecordPath is the data-side mirror of the repository's `.le/workspace.yaml`.
func (l *Layout) RecordPath(id workspace.ID) string {
	return filepath.Join(l.WorkspaceDir(id), "workspace.json")
}

// workspaceSubdirs are created when a workspace is first opened.
func (l *Layout) workspaceSubdirs(id workspace.ID) []string {
	return []string{
		l.WorkspaceDir(id),
		l.ArtifactsDir(id),
		l.CacheDir(id),
		l.OpenCodeDir(id),
		l.SlotsDir(id),
		l.TmpDir(id),
	}
}
