// Package session binds a caller to one workspace's storage.
//
// Every interface onto this system — the CLI, the MCP adapter, anything after
// them — has to perform the same sequence before it may touch a workspace's
// data: resolve the pin, open the root, open that workspace's databases, tell
// the root that inference is now serving this workspace, and record the
// workspace's own identity. Four of those five steps are ordinary wiring. The
// fourth is not.
//
// §2.2 clears the previous workspace's saved inference slots on a switch, so
// that neither the contents of one project's cache nor the timing of its hits
// can be observed from another. A command process performs one switch and
// exits, which makes the step easy to mistake for a formality. A long-lived
// server does not: it serves project A, then project B, then A again, and if
// the switch is skipped the second project runs against slots the first one
// warmed.
//
// That is why this is a package rather than a helper beside each caller. A
// safety invariant duplicated in two adapters is one refactor away from being
// enforced in one of them.
package session

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/akynte/local-engineer/internal/store"
	"github.com/akynte/local-engineer/internal/workspace"
)

// Session is one caller bound to one workspace.
type Session struct {
	Workspace *workspace.Workspace
	Root      *store.Root
	Store     *store.Store
}

// ErrNoWorkspace is returned when path is not inside a workspace. It wraps
// workspace.ErrNotAWorkspace so callers can keep matching on that.
type ErrNoWorkspace struct{ Err error }

func (e *ErrNoWorkspace) Error() string { return e.Err.Error() }
func (e *ErrNoWorkspace) Unwrap() error { return e.Err }

// Open binds to the workspace containing path, using the data directory at
// dataDir.
//
// Failure is explicit: a caller that needs a workspace must not silently
// operate on a default one, because "a default one" is another project.
func Open(ctx context.Context, dataDir, path string) (*Session, error) {
	ws, err := workspace.Open(path)
	if err != nil {
		return nil, &ErrNoWorkspace{Err: err}
	}
	root, err := store.OpenRoot(dataDir)
	if err != nil {
		return nil, err
	}
	st, err := root.OpenWorkspace(ctx, ws.ID())
	if err != nil {
		closeQuietly(root) //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.
		return nil, err
	}
	// §2.2: the switch that clears the previous workspace's saved slots. It
	// belongs here, once, for every interface that binds to a workspace.
	if previous, err := root.SwitchTo(ctx, ws.ID()); err != nil {
		closeQuietly(root) //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.
		return nil, err
	} else if previous != "" && previous != ws.ID() {
		slog.Debug("workspace switch: cleared saved slots",
			"previous", previous, "now", ws.ID())
	}
	if err := st.RecordWorkspace(ws); err != nil {
		closeQuietly(root) //nolint:contextcheck // cleanup must not take the request context: a cancelled call would then skip closing the databases.
		return nil, fmt.Errorf("session: recording workspace identity: %w", err)
	}
	return &Session{Workspace: ws, Root: root, Store: st}, nil
}

// Close releases the storage this session opened.
func (s *Session) Close() error {
	if s == nil || s.Root == nil {
		return nil
	}
	return s.Root.CloseAll()
}

// closeQuietly releases a root while another error is already on its way out.
// The open failure is what the caller needs; replacing it with a close failure
// would hide the cause behind a consequence.
func closeQuietly(root *store.Root) {
	if err := root.CloseAll(); err != nil {
		slog.Debug("session: closing the data directory after a failed open", "error", err)
	}
}
