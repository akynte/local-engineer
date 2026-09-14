package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

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
	// §2.2: slots are cleared on workspace switch, so neither cache contents
	// nor cache timing leak between projects. This is the switch: a command has
	// just bound to one workspace, and anything the previous one left behind is
	// now sitting where this one's work will run.
	if previous, err := root.SwitchTo(ctx, ws.ID()); err != nil {
		return nil, nil, nil, err
	} else if previous != "" {
		slog.Debug("workspace switch: cleared saved slots", "previous", previous, "now", ws.ID())
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

// closeRoot closes the data directory and reports a failure on stderr rather
// than discarding it. CloseAll flushes every SQLite write-ahead log, so a
// failure here means data may not have reached disk — silently swallowing that
// in a deferred call is exactly how a corrupt database goes unnoticed.
func closeRoot(cmd *cobra.Command, root *store.Root) {
	if err := root.CloseAll(); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "le: warning: closing the data directory: %v\n", err)
	}
}
