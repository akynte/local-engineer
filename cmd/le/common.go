package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/akynte/local-engineer/internal/config"
	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// openRoot opens the data directory named by --data, $LE_DATA, or /data.
func openRoot() (*store.Root, error) {
	return store.OpenRoot(g.dataDir)
}

// openWorkspace finds the workspace containing the working directory and opens
// its storage. Failure is explicit: a command that needs a workspace must not
// silently operate on a default one.
func openWorkspace(ctx context.Context) (*workspace.Workspace, *store.Root, *store.Store, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return nil, nil, nil, err
	}
	ws, err := workspace.Open(cwd)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("%w\nRun `le workspace init` in the repository root first", err)
	}
	root, err := openRoot()
	if err != nil {
		return nil, nil, nil, err
	}
	st, err := root.OpenWorkspace(ctx, ws.ID())
	if err != nil {
		return nil, nil, nil, err
	}
	if err := st.RecordWorkspace(ws); err != nil {
		return nil, nil, nil, err
	}
	return ws, root, st, nil
}

// loadConfig reads le.yaml from the data directory's config folder.
func loadConfig(root *store.Root) (config.Config, error) {
	return config.Load(root.Layout().ConfigDir())
}

// loadProfile resolves the active hardware profile, returning nil when none is
// configured so callers can say so rather than pretend.
func loadProfile(root *store.Root, cfg config.Config) *config.Profile {
	if cfg.Profile == "" {
		return nil
	}
	p, err := config.LoadProfile(profileDir(root), cfg.Profile)
	if err != nil {
		return nil
	}
	return &p
}

// profileDir is where generated profiles are written and read. Shipped
// profiles are embedded in the binary, so this directory only ever holds the
// operator's own, and one of those always overrides a shipped name.
func profileDir(root *store.Root) string {
	return filepath.Join(root.Layout().ConfigDir(), "profiles")
}

// emitJSON writes a value as indented JSON to stdout.
func emitJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
